package sessions

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"time"
)

// IndexedSessions returns the ids in Copilot's session index, the `sessions` table of
// session-store.db that the Copilot app reads to list CLI sessions. Copilot writes that database
// itself; this reads its files directly, without SQLite, opening them read-only and never
// writing them. Copilot keeps the database in WAL mode, so committed pages are read from the
// write-ahead log first, the way SQLite itself recovers it.
func IndexedSessions(path string) (map[string]bool, error) {
	db, err := openStore(path)
	if err != nil {
		return nil, err
	}
	root := 0
	err = db.scan(1, func(row []any) {
		if len(row) >= 4 && row[0] == "table" && row[1] == "sessions" {
			if n, ok := row[3].(int64); ok {
				root = int(n)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if root == 0 {
		return nil, errors.New("Copilot's session index has no sessions table")
	}
	ids := map[string]bool{}
	err = db.scan(root, func(row []any) {
		if id, ok := row[0].(string); ok {
			ids[id] = true
		}
	})
	return ids, err
}

var errStore = errors.New("Copilot's session index could not be read")

type store struct {
	file     []byte
	wal      map[uint32][]byte // newest committed copy of each page in the write-ahead log
	pageSize int
	usable   int
	pages    uint32
}

// readFile is os.ReadFile; tests replace it to write between the two reads.
var readFile = os.ReadFile

// openStore reads the database and its write-ahead log as one snapshot. Copilot may commit or
// checkpoint while they are read, and a checkpoint between the two reads would hide committed
// rows, so the read repeats until neither file changed while it ran.
func openStore(path string) (*store, error) {
	for attempt := 1; ; attempt++ {
		before := storeFiles(path)
		wal, walErr := readFile(path + "-wal")
		file, err := readFile(path)
		if err != nil {
			return nil, err
		}
		if storeFiles(path) == before && (walErr == nil || errors.Is(walErr, fs.ErrNotExist)) {
			return parseStore(file, wal)
		}
		if attempt == 5 {
			return nil, errors.New("Copilot's session index kept changing while it was read")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// storeFiles is the size and modification time of the database and its write-ahead log.
func storeFiles(path string) [4]int64 {
	var state [4]int64
	for i, p := range []string{path, path + "-wal"} {
		if info, err := os.Stat(p); err == nil {
			state[2*i], state[2*i+1] = info.Size(), info.ModTime().UnixNano()
		}
	}
	return state
}

func parseStore(file, wal []byte) (*store, error) {
	if len(file) < 100 || !bytes.HasPrefix(file, []byte("SQLite format 3\x00")) || binary.BigEndian.Uint32(file[56:]) > 1 {
		return nil, errStore
	}
	s := &store{file: file, pageSize: int(binary.BigEndian.Uint16(file[16:]))}
	if s.pageSize == 1 {
		s.pageSize = 65536
	}
	if s.pageSize < 512 || s.pageSize&(s.pageSize-1) != 0 {
		return nil, errStore
	}
	s.usable = s.pageSize - int(file[20])
	s.pages = uint32(len(file) / s.pageSize)
	s.readWAL(wal)
	return s, nil
}

// readWAL keeps the frames of committed transactions: frames must carry the header's salts and
// a valid running checksum, and only those up to the last commit frame count.
func (s *store) readWAL(wal []byte) {
	if len(wal) < 32 {
		return
	}
	magic := binary.BigEndian.Uint32(wal)
	if magic&^1 != 0x377f0682 || int(binary.BigEndian.Uint32(wal[8:])) != s.pageSize {
		return
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if magic&1 == 1 {
		order = binary.BigEndian
	}
	s0, s1 := walChecksum(order, wal[:24], 0, 0)
	if s0 != binary.BigEndian.Uint32(wal[24:]) || s1 != binary.BigEndian.Uint32(wal[28:]) {
		return
	}
	pending := map[uint32][]byte{}
	s.wal = map[uint32][]byte{}
	for off := 32; off+24+s.pageSize <= len(wal); off += 24 + s.pageSize {
		frame := wal[off : off+24+s.pageSize]
		if !bytes.Equal(frame[8:16], wal[16:24]) {
			break
		}
		s0, s1 = walChecksum(order, frame[:8], s0, s1)
		s0, s1 = walChecksum(order, frame[24:], s0, s1)
		if s0 != binary.BigEndian.Uint32(frame[16:]) || s1 != binary.BigEndian.Uint32(frame[20:]) {
			break
		}
		pending[binary.BigEndian.Uint32(frame)] = frame[24:]
		if size := binary.BigEndian.Uint32(frame[4:]); size != 0 {
			for page, data := range pending {
				s.wal[page] = data
			}
			clear(pending)
			s.pages = size
		}
	}
}

func walChecksum(order binary.ByteOrder, b []byte, s0, s1 uint32) (uint32, uint32) {
	for i := 0; i+8 <= len(b); i += 8 {
		s0 += order.Uint32(b[i:]) + s1
		s1 += order.Uint32(b[i+4:]) + s0
	}
	return s0, s1
}

func (s *store) page(n uint32) ([]byte, error) {
	if n == 0 || n > s.pages {
		return nil, errStore
	}
	if p, ok := s.wal[n]; ok {
		return p, nil
	}
	off := int(n-1) * s.pageSize
	if off+s.pageSize > len(s.file) {
		return nil, errStore
	}
	return s.file[off : off+s.pageSize], nil
}

// scan visits every row of the table b-tree rooted at page root.
func (s *store) scan(root int, visit func([]any)) error {
	seen := map[uint32]bool{}
	var walk func(uint32) error
	walk = func(n uint32) error {
		if seen[n] || len(seen) > 1<<20 {
			return errStore
		}
		seen[n] = true
		p, err := s.page(n)
		if err != nil {
			return err
		}
		h := 0
		if n == 1 {
			h = 100
		}
		cells := int(binary.BigEndian.Uint16(p[h+3:]))
		switch p[h] {
		case 0x05: // interior table page
			for i := range cells {
				ptr := h + 12 + 2*i
				if ptr+2 > len(p) {
					return errStore
				}
				cell := int(binary.BigEndian.Uint16(p[ptr:]))
				if cell+4 > len(p) {
					return errStore
				}
				if err := walk(binary.BigEndian.Uint32(p[cell:])); err != nil {
					return err
				}
			}
			return walk(binary.BigEndian.Uint32(p[h+8:]))
		case 0x0d: // leaf table page
			for i := range cells {
				ptr := h + 8 + 2*i
				if ptr+2 > len(p) {
					return errStore
				}
				payload, err := s.payload(p, int(binary.BigEndian.Uint16(p[ptr:])))
				if err != nil {
					return err
				}
				row, err := record(payload)
				if err != nil {
					return err
				}
				visit(row)
			}
			return nil
		}
		return errStore
	}
	return walk(uint32(root))
}

// payload reads one leaf cell's record, following overflow pages.
func (s *store) payload(p []byte, cell int) ([]byte, error) {
	size, n := varint(p, cell)
	if n == 0 {
		return nil, errStore
	}
	cell += n
	if _, n = varint(p, cell); n == 0 { // rowid
		return nil, errStore
	}
	cell += n
	total, max := int(size), s.usable-35
	local := total
	if total > max {
		min := (s.usable-12)*32/255 - 23
		local = min + (total-min)%(s.usable-4)
		if local > max {
			local = min
		}
	}
	if total < 0 || cell+local > len(p) {
		return nil, errStore
	}
	out := append([]byte(nil), p[cell:cell+local]...)
	if local == total {
		return out, nil
	}
	if cell+local+4 > len(p) {
		return nil, errStore
	}
	next := binary.BigEndian.Uint32(p[cell+local:])
	for len(out) < total {
		page, err := s.page(next)
		if err != nil {
			return nil, err
		}
		chunk := min(total-len(out), s.usable-4)
		out = append(out, page[4:4+chunk]...)
		next = binary.BigEndian.Uint32(page)
	}
	return out, nil
}

// record decodes a row: text, integers and NULLs; other values are left as nil.
func record(b []byte) ([]any, error) {
	size, n := varint(b, 0)
	if n == 0 || int(size) > len(b) {
		return nil, errStore
	}
	var row []any
	body := int(size)
	for at := n; at < int(size); {
		t, n := varint(b, at)
		if n == 0 {
			return nil, errStore
		}
		at += n
		width := 0
		switch {
		case t >= 12:
			width = int(t-12) / 2
		case t >= 1 && t <= 4:
			width = int(t)
		case t == 5:
			width = 6
		case t == 6 || t == 7:
			width = 8
		}
		if body+width > len(b) {
			return nil, errStore
		}
		v := b[body : body+width]
		switch {
		case t >= 13 && t%2 == 1:
			row = append(row, string(v))
		case t >= 1 && t <= 6:
			x := int64(int8(v[0]))
			for _, c := range v[1:] {
				x = x<<8 | int64(c)
			}
			row = append(row, x)
		case t == 8:
			row = append(row, int64(0))
		case t == 9:
			row = append(row, int64(1))
		default:
			row = append(row, nil)
		}
		body += width
	}
	return row, nil
}

func varint(b []byte, at int) (uint64, int) {
	var v uint64
	for i := 0; i < 9; i++ {
		if at+i >= len(b) {
			return 0, 0
		}
		c := b[at+i]
		if i == 8 {
			return v<<8 | uint64(c), 9
		}
		v = v<<7 | uint64(c&0x7f)
		if c < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}
