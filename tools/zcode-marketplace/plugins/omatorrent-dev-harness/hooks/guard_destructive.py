#!/usr/bin/env python3
"""PreToolUse guard for the Bash tool: deny obviously destructive development
commands. Development-workflow safety only; this is NOT product security.

Stdin : one JSON object (ZCode hook contract) with tool_name, tool_input
       (tool_input.command holds the shell command) and cwd.
Exit 0: allow (also on ANY internal error - the guard fails open so a hook
       bug can never break the session).
Exit 2: deny; the reason on stderr is shown to the agent.

Human override (deliberate use only): set OT_ALLOW_DESTRUCTIVE=1 as an env
prefix in the command, or append the comment  # ot-allow-destructive .
Agents must ask the user before using the override.
"""
import json
import os
import re
import shlex
import sys

OVERRIDE_RE = re.compile(r"OT_ALLOW_DESTRUCTIVE=1|#\s*ot-allow-destructive\b")

# System trees an agent must never recursively delete or re-permission.
PROTECTED_ROOTS = (
    "/boot", "/etc", "/usr", "/var", "/home", "/opt", "/srv",
    "/root", "/run", "/lib", "/lib64", "/bin", "/sbin",
    "/dev", "/proc", "/sys", "/efi",
)

# Device/filesystem destroyers: always denied regardless of target.
DEVICE_KILLERS = (
    r"\bmkfs(\.\w+)?\b",
    r"\bwipefs\b",
    r"\bblkdiscard\b",
    r"\bshred\b",
    r"\bdd\b[^|;&]*\bof=/dev/",
)

# Git history/work destroyers. --force-with-lease and --force-if-includes
# are deliberately allowed (they are the safer push variants), as is
# `git restore --staged .` (index only, no worktree loss).
GIT_BLOCKERS = (
    r"\bgit\s+reset\s+--hard\b",
    r"\bgit\s+clean\b[^|;&]*(\s-[a-zA-Z]*f|\s--force\b)",
    r"\bgit\s+checkout\s+(--\s+)?\.(?:\s|$)",
    r"\bgit\s+restore\b(?![^|;&]*--staged)[^|;&]*\s\.(?:\s|$)",
    r"\bgit\s+restore\b[^|;&]*--worktree[^|;&]*\s\.(?:\s|$)",
    r"\bgit\s+push\b[^|;&]*(\s--force\b(?!-)|\s-f\b|\s--delete\b|\s-d\b"
    r"|\s\+[a-zA-Z0-9_./:~-]+:|\s:[a-zA-Z0-9_./-])",
    r"\bgit\s+tag\b[^|;&]*\s(--delete|-d)\b",
    r"\bgit\s+branch\s+-D\b",
)

# Crude fallback used when shell tokenization fails (e.g. unterminated
# quotes): recursive+forced rm combined with a clearly protected target.
FALLBACK_RM_RE = re.compile(
    r"\brm\b[^|;&]*(\s-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\b|\s-[a-zA-Z]*f[a-zA-Z]*r[a-zA-Z]*\b"
    r"|\s-r\b[^|;&]*\s-f\b|\s-f\b[^|;&]*\s-r\b"
    r"|\s--recursive\b[^|;&]*\s--force\b|\s--force\b[^|;&]*\s--recursive\b)"
)
FALLBACK_TARGET_RE = re.compile(
    r"(~|\$HOME|\.git\b|/(?:(?:home|etc|usr|var|opt|srv|root|boot|run|lib"
    r"|lib64|bin|sbin|dev|proc|sys|efi)(?:/|\s|$)))"
)

SEGMENT_SEPS = {"&&", "||", ";", "|", "\n"}


def deny(reason: str) -> "None":
    sys.stderr.write(
        "ot-destructive-guard: DENIED - {}. If the user explicitly approved "
        "this, rerun with OT_ALLOW_DESTRUCTIVE=1 as env prefix or append "
        "the comment '# ot-allow-destructive'.\n".format(reason)
    )
    sys.exit(2)


def resolve(target: str, cwd: str, home: str) -> str:
    t = target.strip()
    if t.startswith("~"):
        t = home + t[1:]
    elif t.startswith("$HOME"):
        t = home + t[5:]
    if not t.startswith("/"):
        t = os.path.normpath(os.path.join(cwd, t))
    return os.path.normpath(t)


def path_is_outside_workspace(path: str, cwd: str) -> bool:
    if path == cwd or path.startswith(cwd + os.sep):
        return False
    if path == os.path.expanduser("~"):
        return True
    for root in PROTECTED_ROOTS:
        if path == root or path.startswith(root + os.sep):
            return True
    return False  # e.g. /tmp scratch paths are allowed


def check_rm(targets: "list", has_recursive: bool, has_force: bool, cwd: str) -> "None":
    for raw in targets:
        # Deleting the repository itself (its .git) destroys history.
        if raw.rstrip("/").endswith("/.git") or raw == ".git":
            deny("rm targets the .git directory (repository destruction)")
        if not (has_recursive and has_force):
            continue
        p = resolve(raw, cwd, os.path.expanduser("~"))
        if p == "/" or p == "/*":
            deny("rm -rf targets the filesystem root")
        if path_is_outside_workspace(p, cwd):
            deny(
                "rm -rf targets '{}' outside the current workspace "
                "or inside a protected system tree".format(raw)
            )


def check_chmod_chown(segment: "list", cwd: str) -> "None":
    if not any("-R" in tok and re.fullmatch(r"-[a-zA-Z]*R[a-zA-Z]*", tok)
               for tok in segment):
        return
    idx = next((i for i, t in enumerate(segment) if t in ("chmod", "chown")), None)
    if idx is None:
        return
    for raw in segment[idx + 1:]:
        if not raw.startswith("-") and raw not in ("--recursive",):
            p = resolve(raw, cwd, os.path.expanduser("~"))
            if path_is_outside_workspace(p, cwd):
                deny(
                    "recursive chmod/chown on '{}' outside the current "
                    "workspace or inside a protected system tree".format(raw)
                )


def fallback_rm_check(cmd: str) -> None:
    if FALLBACK_RM_RE.search(cmd) and FALLBACK_TARGET_RE.search(cmd):
        deny("recursive forced rm with a protected target (fallback check)")


def main() -> int:
    try:
        data = sys.stdin.read()
        if not data.strip():
            return 0
        try:
            event = json.loads(data)
        except ValueError:
            event = json.loads(next(
                (ln for ln in data.splitlines() if ln.strip()), ""))
    except Exception:
        return 0  # fail open

    try:
        tool_name = event.get("tool_name") or event.get("toolName") or ""
        payload = event.get("tool_input") or event.get("toolInput") or {}
        if isinstance(payload, str):
            try:
                payload = json.loads(payload)
            except Exception:
                payload = {}
        cmd = payload.get("command", "") if isinstance(payload, dict) else ""
        cwd = event.get("cwd") or os.getcwd()
        cwd = os.path.normpath(os.path.expanduser(str(cwd)))
    except Exception:
        return 0  # fail open

    if not cmd or (tool_name and tool_name != "Bash"):
        return 0
    if OVERRIDE_RE.search(cmd):
        return 0

    try:
        for pattern in DEVICE_KILLERS:
            if re.search(pattern, cmd):
                deny("command matches destructive pattern {!r}".format(pattern))
        for pattern in GIT_BLOCKERS:
            if re.search(pattern, cmd):
                deny(
                    "command destroys git history or uncommitted work "
                    "({!r})".format(pattern)
                )

        # Tokenize per shell segment so flags/targets are attributed to the
        # right command; when parsing fails (e.g. unterminated quotes), run
        # the crude recursive-rm fallback instead of skipping entirely.
        try:
            tokens = shlex.split(cmd)
        except ValueError:
            fallback_rm_check(cmd)
            return 0
        segment = []
        segments = []
        for tok in tokens:
            if tok in SEGMENT_SEPS or tok.endswith(";"):
                segments.append(segment)
                segment = []
            else:
                segment.append(tok)
        segments.append(segment)

        for seg in segments:
            if "rm" in seg:
                i = seg.index("rm")
                rest = seg[i + 1:]
                flags = "".join(
                    t[1:] for t in rest if re.fullmatch(r"-[a-zA-Z]+", t)
                )
                has_r = "r" in flags or "R" in flags or "--recursive" in rest
                has_f = "f" in flags or "--force" in rest
                targets = [
                    t for t in rest
                    if not t.startswith("-") and "=" not in t.split("/", 1)[0]
                ]
                check_rm(targets, has_r, has_f, cwd)
            if "chmod" in seg or "chown" in seg:
                check_chmod_chown(seg, cwd)
    except SystemExit:
        raise
    except Exception as exc:  # diagnostics, never a silent break
        sys.stderr.write("ot-destructive-guard: internal error (allowed): {}\n".format(exc))
        return 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
