import React, { useEffect, useRef, useState } from 'react';
import MarkGithubIcon from '@primer/octicons-react/MarkGithubIcon';
import CopilotIcon from '@primer/octicons-react/CopilotIcon';
import ArrowUpRightIcon from '@primer/octicons-react/ArrowUpRightIcon';
import DownloadIcon from '@primer/octicons-react/DownloadIcon';
import CheckIcon from '@primer/octicons-react/CheckIcon';
import SyncIcon from '@primer/octicons-react/SyncIcon';
import FileCodeIcon from '@primer/octicons-react/FileCodeIcon';
import ShieldCheckIcon from '@primer/octicons-react/ShieldCheckIcon';
import GitBranchIcon from '@primer/octicons-react/GitBranchIcon';
import './styles.css';

function Brand() {
  return <a className="brand" href="/" aria-label="Copilot Ready home"><span className="brand-symbol" aria-hidden="true">↗</span><span>copilot<span className="brand-slash">/</span><strong>ready</strong></span></a>;
}

function Header() {
  return <header className="site-header shell"><Brand /><nav aria-label="Main navigation"><a href="#how-it-works">How it works</a><a className="nav-cta" href="#setup">Get started <ArrowUpRightIcon verticalAlign={null} size={16} /></a></nav></header>;
}

function PlatformIcon({ platform }) {
  return platform === 'windows' ? <svg aria-hidden="true" viewBox="0 0 20 20" width="17" height="17" fill="currentColor"><path d="M2 3.5 9 2.5V9H2zm8-1.2 8-1.1V9h-8zM2 10h7v6.5l-7-1zm8 0h8v7.8l-8-1.1z" /></svg> : <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="currentColor"><path d="M17.1 12.6c0-2.1 1.7-3.2 1.8-3.3-1-1.5-2.6-1.7-3.1-1.7-1.4-.1-2.7.8-3.4.8-.7 0-1.8-.8-3-.8-1.5 0-2.9.9-3.7 2.2-1.6 2.7-.4 6.8 1.1 9 .8 1.1 1.6 2.2 2.8 2.2 1.1 0 1.6-.7 3-.7 1.4 0 1.8.7 3 .7 1.2 0 2-1.1 2.8-2.2.9-1.3 1.3-2.6 1.3-2.7-.1 0-2.6-1-2.6-3.5ZM14.7 6.2c.6-.8 1.1-1.9 1-3.1-1 .1-2.2.7-2.9 1.4-.7.8-1.3 1.9-1.1 3.1 1.1.1 2.3-.6 3-1.4Z" /></svg>;
}

const platforms = {
  windows: { label: 'Windows', file: '/downloads/Install-Copilot-Windows.zip', armFile: '/downloads/Install-Copilot-Windows-ARM64.zip', detail: 'Windows x64 & ARM64', hint: 'Extract the ZIP, then right-click Install Copilot.ps1 and choose Run with PowerShell. On Windows 11, choose Show more options first.', next: 'Extract the ZIP, then right-click Install Copilot.ps1 and choose Run with PowerShell (on Windows 11, under Show more options). Keep its window open. The setup page will guide you from there.' },
  mac: { label: 'macOS', file: '/downloads/Install-Copilot-macOS.zip', detail: 'Apple Silicon & Intel', hint: 'Unzip the download, then open Install Copilot.command.', next: 'Open the ZIP, then open Install Copilot.command. Keep Terminal open. If prompted, approve it in Privacy & Security.' },
};

function DownloadPanel() {
  const [platform, setPlatform] = useState('windows');
  const [downloaded, setDownloaded] = useState(false);
  const [arm, setArm] = useState(false);
  const tabs = useRef({});
  useEffect(() => { if (/Macintosh|MacIntel/.test(navigator.userAgent + navigator.platform)) setPlatform('mac'); }, []);
  // Chromium browsers report the processor; elsewhere the x64 download is offered, which also runs on ARM.
  useEffect(() => { navigator.userAgentData?.getHighEntropyValues?.(['architecture']).then(v => setArm(v.platform === 'Windows' && v.architecture === 'arm')).catch(() => {}); }, []);
  const chosen = platforms[platform];
  function choose(value, focus = false) {
    setPlatform(value); setDownloaded(false);
    if (focus) tabs.current[value]?.focus();
  }
  function moveTab(event) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    choose(event.key === 'Home' ? 'windows' : event.key === 'End' ? 'mac' : platform === 'windows' ? 'mac' : 'windows', true);
  }
  return <div className="download-panel" id="setup">
    <div className="platform-line"><div className="platform-tabs" role="tablist" aria-label="Choose your computer">
      {Object.entries(platforms).map(([key, value]) => <button key={key} ref={node => { tabs.current[key] = node; }} type="button" role="tab" id={`${key}-tab`} aria-controls="download-choice" aria-selected={platform === key} tabIndex={platform === key ? 0 : -1} className={platform === key ? 'platform active' : 'platform'} onClick={() => choose(key)} onKeyDown={moveTab}><PlatformIcon platform={key} />{value.label}</button>)}
    </div><span className="platform-detail">{chosen.detail}</span></div>
    <div id="download-choice" role="tabpanel" aria-labelledby={`${platform}-tab`}>
      <a className="primary-button download-button" href={platform === 'windows' && arm ? chosen.armFile : chosen.file} download onClick={() => setDownloaded(true)}><DownloadIcon verticalAlign={null} size={20} /><span>Download for {platform === 'mac' ? 'Mac' : 'Windows'}</span><ArrowUpRightIcon verticalAlign={null} size={20} /></a>
      <p className="download-hint">{chosen.hint}</p>
      {platform === 'windows' && <p className="download-hint">Unsigned helper: if Windows says it protected your PC, choose <strong>More info → Run anyway</strong>. If the PowerShell window closes straight away, open <strong>copilot-ready.exe</strong> from the extracted folder instead.</p>}
      {platform === 'windows' && <p className="download-hint">{arm ? <>Not an ARM PC? <a href={chosen.file} download onClick={() => setDownloaded(true)}>Download for x64</a>.</> : <>ARM PC, such as a Surface Pro X or Copilot+ PC? <a href={chosen.armFile} download onClick={() => setDownloaded(true)}>Download for ARM64</a>.</>}</p>}
      {platform === 'mac' && <p className="download-hint">Unsigned installer: if macOS blocks it, go to <strong>System Settings → Privacy &amp; Security → Open Anyway</strong>, then confirm <strong>Open</strong>.</p>}
      {downloaded ? <div className="download-next" role="status"><CheckIcon verticalAlign={null} size={18} /><div><strong>Your next step</strong><p>{chosen.next}</p></div></div> : null}
    </div>
    <p className="download-disclosure">Packaging demo only. No hosted instance or release downloads are provided by this repository.</p>
  </div>;
}

function Journey() {
  const [replay, setReplay] = useState(0);
  return <figure className="journey" aria-label="Illustration: your existing setup moving toward Copilot">
    <div className="journey-top"><span><span className="tiny-dot" /> YOUR NEXT WORKSPACE</span><span>01 — 03</span></div>
    <div className="journey-visual" key={replay} aria-hidden="true">
      <div className="source-names"><span><span className="codex-star">✳</span> Codex</span><span><span className="claude-star">✳</span> Claude Code</span></div>
      <div className="journey-rail"><div className="rail-line" /><div className="rail-signal" /><div className="octocat-traveler"><MarkGithubIcon verticalAlign={null} size={30} /></div><div className="copilot-dock"><CopilotIcon verticalAlign={null} size={40} /></div><span className="rail-label start">Your setup</span><span className="rail-label end">Copilot</span></div>
      <div className="journey-review"><div className="review-title"><span><GitBranchIcon verticalAlign={null} size={16} /> A familiar starting point</span><span className="preview-tag">PREVIEW</span></div><div className="preview-row"><FileCodeIcon verticalAlign={null} size={17} /><span>Your instructions</span><span className="mini-pill">Bring along</span></div><div className="preview-row"><span className="spark-symbol">✦</span><span>Your favorite skills</span><span className="mini-pill">Bring along</span></div><div className="preview-row"><ShieldCheckIcon verticalAlign={null} size={17} /><span>Your final say</span><CheckIcon verticalAlign={null} size={16} className="green" /></div></div>
      <div className="journey-caption"><span className="tiny-dot" /> Familiar setup. Fresh possibilities.</div>
    </div>
    <figcaption><span>Your setup, moving with you.</span><button className="replay-button" onClick={() => setReplay(v => v + 1)} aria-label="Replay Octocat journey"><SyncIcon verticalAlign={null} size={14} /> Replay</button></figcaption>
  </figure>;
}

const steps = [
  { id: '01', name: 'Find', title: 'Find your setup.', text: 'Discover your instructions, skills, and tools.', icon: FileCodeIcon, detail: 'Copilot · Codex · Claude Code' },
  { id: '02', name: 'Choose', title: 'Keep what you want.', text: 'Ready items are selected. Deselect anything you don’t need.', icon: ShieldCheckIcon, detail: 'See what moves. See what needs work.' },
  { id: '03', name: 'Go', title: 'Open your project.', text: 'Apply your choices, sign in, and get back to work.', icon: ArrowUpRightIcon, detail: 'Your setup. Ready to go.' },
];

function Landing() {
  return <main id="main-content">
    <section className="hero shell">
      <div className="hero-copy"><p className="eyebrow"><span className="status-dot" /> NEW TOOL. SAME YOU.</p><h1>Your setup.<br />Ready for <span>Copilot.</span></h1><p className="hero-intro">Bring your settings from Codex and Claude Code.<br className="desktop-break" /> Choose what to keep. Make Copilot feel like yours.</p><DownloadPanel /></div>
      <Journey />
    </section>
    <section id="how-it-works" className="story shell" aria-labelledby="story-title"><div className="section-intro"><div><p className="eyebrow purple">THREE STEPS</p><h2 id="story-title">Make the move.</h2></div></div><div className="steps">{steps.map(({ id, name, title, text, icon: Icon, detail }) => <article className="step" key={id}><div className="step-marker"><span>{id}</span><div><Icon verticalAlign={null} size={22} /></div><span>{name}</span></div><h3>{title}</h3><p>{text}</p><div className="step-detail">{detail}</div></article>)}</div></section>
  </main>;
}

function Footer() {
  return <footer className="site-footer shell"><div><Brand /><p>Independent setup for Copilot.</p></div><div className="footer-links"><a href="/downloads/copilot-ready-source.zip" download>Source <ArrowUpRightIcon verticalAlign={null} size={13} /></a><a href="/downloads/manifest.json">Checksums</a></div><div className="footer-note">Windows + macOS</div></footer>;
}

export default function App() {
  return <><a className="skip-link" href="#main-content">Skip to content</a><Header /><Landing /><Footer /></>;
}
