#!/usr/bin/env bash
# Behavioral test for the omatorrent-dev-harness destructive-command guard.
# Feeds hook-event JSON to guard_destructive.py and asserts exit codes:
# 0 = allowed, 2 = denied. Run from anywhere; uses the repo's guard script.
set -u
GUARD="$(cd "$(dirname "$0")" && pwd)/zcode-marketplace/plugins/omatorrent-dev-harness/hooks/guard_destructive.py"
WORKSPACE="$(cd "$(dirname "$0")/.." && pwd)"
pass=0; fail=0

run_case() { # $1 expected_exit(0|2)  $2 label  $3 command string
  local expected="$1" label="$2" cmd="$3" got
  got=$(printf '{"tool_name":"Bash","tool_input":{"command":%s},"cwd":"%s"}' \
        "$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$cmd")" \
        "$WORKSPACE" | python3 "$GUARD" >/dev/null 2>&1; echo $?)
  if [ "$got" = "$expected" ]; then
    pass=$((pass+1)); printf 'PASS  %-38s -> %s\n' "$label" "$got"
  else
    fail=$((fail+1)); printf 'FAIL  %-38s expected %s got %s\n' "$label" "$expected" "$got"
  fi
}

# Ordinary engineering commands must be allowed
run_case 0 "plain git status"            "git status --short"
run_case 0 "build test run"              "go test ./..."
run_case 0 "relative rm -rf build dir"   "rm -rf build"
run_case 0 "tmp scratch rm -rf"          "rm -rf /tmp/omatorrent-scratch"
run_case 0 "force-with-lease push"       "git push --force-with-lease origin main"
run_case 0 "git add/commit"              "git add -A && git commit -m 'wip'"
run_case 0 "mkdir nested"                "mkdir -p docs/adr"

# Destructive commands must be denied
run_case 0 "checkout branch"             "git checkout main"
run_case 0 "checkout new branch"         "git checkout -b feature/x"
run_case 0 "restore staged (index only)" "git restore --staged ."

# Destructive commands must be denied
run_case 2 "rm -rf /"                    "rm -rf /"
run_case 2 "rm -rf system tree"          "rm -rf /etc/nginx"
run_case 2 "rm -rf home path"            "rm -rf $HOME/omatorrent-guard-test"
run_case 2 "rm -rf tilde path"           "rm -rf ~/old"
run_case 2 "rm -rf .git"                 "rm -rf .git"
run_case 2 "rm -rf path/.git"            "rm -rf tools/.git"
run_case 2 "git reset --hard"            "git reset --hard HEAD~2"
run_case 2 "git clean -fdx"              "git clean -fdx"
run_case 2 "git checkout discard all"    "git checkout -- ."
run_case 2 "git checkout discard (no --)" "git checkout ."
run_case 2 "git restore discard all"     "git restore ."
run_case 2 "git restore worktree dot"    "git restore --staged --worktree ."
run_case 2 "force push"                  "git push --force origin main"
run_case 2 "short force push"            "git push -f origin main"
run_case 2 "push delete branch"          "git push origin --delete main"
run_case 2 "push empty refspec delete"   "git push origin :main"
run_case 2 "push plus-refspec force"     "git push origin +main:main"
run_case 2 "git clean long --force"      "git clean --force -d"
run_case 2 "git tag delete"              "git tag -d v1.0"
run_case 2 "git branch -D"               "git branch -D feature"
run_case 2 "mkfs"                        "mkfs.ext4 /dev/sda1"
run_case 2 "dd to device"                "dd if=/dev/zero of=/dev/sda bs=1M count=1"
run_case 2 "shred"                       "shred /dev/sda"
run_case 2 "recursive chmod on /etc"     "chmod -R 755 /etc"
run_case 2 "rm -rf via broken quotes"    "rm -rf /etc/nginx && echo 'unterminated"

# Human override markers must allow
run_case 0 "override env prefix"         "OT_ALLOW_DESTRUCTIVE=1 git reset --hard"
run_case 0 "override comment"            "git reset --hard HEAD~2 # ot-allow-destructive"

# Fail-open on malformed input
echo 'not json at all' | python3 "$GUARD" >/dev/null 2>&1
if [ $? = 0 ]; then pass=$((pass+1)); echo "PASS  malformed stdin                   -> 0"; else fail=$((fail+1)); echo "FAIL  malformed stdin"; fi
: | python3 "$GUARD" >/dev/null 2>&1
if [ $? = 0 ]; then pass=$((pass+1)); echo "PASS  empty stdin                      -> 0"; else fail=$((fail+1)); echo "FAIL  empty stdin"; fi

echo; echo "guard hook: $pass passed, $fail failed"
[ "$fail" = 0 ]
