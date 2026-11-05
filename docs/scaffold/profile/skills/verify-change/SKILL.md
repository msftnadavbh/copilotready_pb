---
name: verify-change
description: Review a completed code change and run the relevant project checks before reporting it ready.
user-invocable: true
disable-model-invocation: true
---

Read the project instructions and changed files. Identify the actual behavior changed and the smallest existing check that exercises it. Run the appropriate documented checks, inspect their results, and report failures accurately. Do not install dependencies, alter security policy, or execute unfamiliar project scripts without the permissions required by the active session. Distinguish tests run from checks that remain unavailable.
