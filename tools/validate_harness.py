#!/usr/bin/env python3
"""Validate the omatorrent-dev-harness ZCode plugin and project docs.

Checks (bootstrap contract, docs/HARNESS.md):
  JSON syntax (marketplace/plugin/hooks), agent frontmatter (supported keys,
  valid tool names, positive maxTurns), skill frontmatter (name==dir,
  description<=1024), command files (description, kebab-case names), hooks
  schema (valid events, compilable matchers, type-correct fields, referenced
  scripts exist), marketplace source paths resolve, referenced relative
  paths in AGENTS.md/ADR index exist, no machine-specific absolute paths,
  no secret-like assignments, no product code in the harness.

Exit code 0 = all checks PASS; 1 = any FAIL. Each check prints PASS/FAIL
with details. Uses only the Python standard library.
"""
import json
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
MP = REPO / "tools/zcode-marketplace"
PLUGIN = MP / "plugins/omatorrent-dev-harness"

SUPPORTED_AGENT_KEYS = {
    "name", "description", "model", "thoughtLevel", "color", "tools",
    "disallowedTools", "maxTurns", "injectAgentsMd", "mcpServers",
}
VALID_TOOLS = {
    "Read", "Grep", "Glob", "Bash", "Edit", "Write",
    "WebFetch", "WebSearch", "TodoWrite",
}
VALID_EVENTS = {
    "SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest",
    "PostToolUse", "PostToolUseFailure", "Stop",
}
COMMAND_HOOK_KEYS = {"command", "shell", "timeout", "timeoutMs", "type",
                     "statusMessage", "enabled", "async"}
PROCESS_HOOK_KEYS = {"command", "args", "timeoutMs", "type",
                     "statusMessage", "enabled"}
SECRET_RE = re.compile(
    r"(password|passwd|secret|token|api[_-]?key)\s*[:=]\s*\S+", re.I)
PLACEHOLDER_RE = re.compile(r"(changeme|example[_-]?pass|hunter2|TODO-SECRET)", re.I)

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print("{} {}{}".format("PASS" if ok else "FAIL", name,
                           (" - " + detail) if detail and not ok else ""))


def parse_frontmatter(text):
    """Minimal parser for our frontmatter subset: `key: value` and `- item`
    lists. Returns (dict, body_start_index) or None when absent/unclosed."""
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        return None
    fm, last_list = {}, None
    for i in range(1, len(lines)):
        line = lines[i]
        if line.strip() == "---":
            return fm
        m = re.match(r"^\s+-\s+(\S.*)$", line)
        if m and last_list is not None and isinstance(fm.get(last_list), list):
            fm[last_list].append(m.group(1).strip())
            continue
        m = re.match(r"^([A-Za-z][A-Za-z0-9_]*):\s*(.*)$", line)
        if m:
            key, val = m.group(1), m.group(2).strip()
            if val == "":
                fm[key] = []
                last_list = key
            else:
                fm[key] = val.strip("\"'")
                last_list = None
    return None  # no closing delimiter


def main():
    # 1-2. JSON syntax
    json_files = [MP / "marketplace.json", PLUGIN / ".zcode-plugin/plugin.json",
                  PLUGIN / "hooks/hooks.json"]
    parsed = {}
    for jf in json_files:
        try:
            parsed[jf] = json.loads(jf.read_text())
            check("json:syntax {}".format(jf.name), True)
        except Exception as exc:
            check("json:syntax {}".format(jf.name), False, str(exc))
            return 1

    # 3. marketplace: name pattern, plugin source path resolves
    mp = parsed[MP / "marketplace.json"]
    check("marketplace:has name", bool(mp.get("name")))
    entries = mp.get("plugins", [])
    check("marketplace:one plugin entry", len(entries) == 1)
    src = entries[0].get("source", "") if entries else ""
    src_path = (MP / src).resolve()
    check("marketplace:source resolves", src_path.is_dir(), str(src_path))
    check("marketplace:entry name matches manifest",
          entries[0].get("name") == parsed[PLUGIN / ".zcode-plugin/plugin.json"].get("name"))

    manifest = parsed[PLUGIN / ".zcode-plugin/plugin.json"]
    check("plugin:name pattern", bool(re.fullmatch(
        r"[a-z0-9][a-z0-9._-]{0,127}", manifest.get("name", ""))))
    check("plugin:version present", bool(manifest.get("version")))
    for comp in ("commands", "skills"):
        p = PLUGIN / manifest.get(comp, comp)
        check("plugin:{} dir exists".format(comp), p.is_dir(), str(p))

    # 4. agents
    agent_files = sorted((PLUGIN / "agents").glob("*.md"))
    check("agents:six files", len(agent_files) == 6,
          "found {}".format(len(agent_files)))
    for af in agent_files:
        fm = parse_frontmatter(af.read_text())
        ok = fm is not None and fm.get("name") and fm.get("description")
        check("agent:frontmatter {}".format(af.name), bool(ok))
        if not ok:
            continue
        bad_keys = set(fm) - SUPPORTED_AGENT_KEYS
        check("agent:keys supported {}".format(af.name), not bad_keys,
              str(bad_keys))
        tools = fm.get("tools", [])
        if isinstance(tools, str):
            tools = [tools]
        bad_tools = [t for t in tools if t not in VALID_TOOLS]
        check("agent:tool names {}".format(af.name), not bad_tools, str(bad_tools))
        mt = fm.get("maxTurns")
        check("agent:maxTurns positive int {}".format(af.name),
              bool(re.fullmatch(r"\d+", str(mt))) and int(mt) > 0, str(mt))

    # 5. skills: flat layout, name==dirname, description length
    skill_files = sorted((PLUGIN / "skills").glob("*/SKILL.md"))
    check("skills:six files", len(skill_files) == 6,
          "found {}".format(len(skill_files)))
    nested = [p for p in (PLUGIN / "skills").glob("*/*/*/SKILL.md")]
    check("skills:flat layout", not nested, str(nested))
    for sf in skill_files:
        fm = parse_frontmatter(sf.read_text())
        name_ok = fm and fm.get("name") == sf.parent.name
        desc = fm.get("description", "") if fm else ""
        check("skill:name==dir {}".format(sf.parent.name), bool(name_ok))
        check("skill:description<=1024 {}".format(sf.parent.name),
              0 < len(desc) <= 1024, str(len(desc)))

    # 6. commands: kebab-case filenames, description frontmatter, $ARGUMENTS
    cmd_files = sorted((PLUGIN / "commands").glob("*.md"))
    check("commands:seven files", len(cmd_files) == 7,
          "found {}".format(len(cmd_files)))
    for cf in cmd_files:
        stem = cf.stem
        check("command:kebab-name {}".format(stem),
              bool(re.fullmatch(r"[a-z0-9]+(-[a-z0-9]+)*", stem)))
        fm = parse_frontmatter(cf.read_text())
        check("command:description {}".format(stem),
              bool(fm and fm.get("description")))

    # 7. hooks: events, matchers, types, script existence
    hooks = parsed[PLUGIN / "hooks/hooks.json"].get("hooks", {})
    bad_events = set(hooks) - VALID_EVENTS
    check("hooks:valid events", not bad_events, str(bad_events))
    hook_scripts = []
    for event, groups in hooks.items():
        for g in groups:
            matcher = g.get("matcher", "")
            try:
                re.compile(matcher) if matcher else None
                mok = True
            except re.error:
                mok = False
            check("hooks:matcher compiles [{}]".format(event), mok, matcher)
            for h in g.get("hooks", []):
                htype = h.get("type")
                if htype == "command":
                    allowed = COMMAND_HOOK_KEYS
                elif htype == "process":
                    allowed = PROCESS_HOOK_KEYS
                else:
                    allowed = set()
                check("hooks:type known [{}]".format(event), htype in
                      ("command", "process"), str(htype))
                extra = set(h) - allowed
                check("hooks:fields for type [{}]".format(event),
                      not extra, "extra: {}".format(extra))
                cmd_str = h.get("command", "")
                check("hooks:command nonempty [{}]".format(event),
                      bool(cmd_str))
                m = re.search(r"\$\{(?:ZCODE|CLAUDE)_PLUGIN_ROOT\}/(\S+?)\"",
                              cmd_str)
                if m:
                    rel = m.group(1)
                    hook_scripts.append(rel)
                    check("hooks:script exists [{}] {}".format(event, rel),
                          (PLUGIN / rel).is_file())
    check("hooks:two hook entries", sum(len(v) for v in hooks.values()) == 2)

    # 8. AGENTS.md + ADR index referenced paths exist
    agents_md = (REPO / "AGENTS.md").read_text()
    refs = set(re.findall(r"`((?:docs|tools)/[A-Za-z0-9_./-]+)`", agents_md))
    missing = [r for r in sorted(refs) if not (REPO / r.rstrip("/")).exists()]
    check("AGENTS.md:referenced paths exist", not missing, str(missing))
    adr_index = (REPO / "docs/adr/README.md").read_text()
    refs = set(re.findall(r"\]\((\d{4}-[a-z-]+\.md)\)", adr_index))
    missing = [r for r in sorted(refs) if not (REPO / "docs/adr" / r).exists()]
    check("ADR index:linked files exist", not missing, str(missing))

    # 9. docs files all present
    expected_docs = ["PRODUCT", "ARCHITECTURE", "ROADMAP", "SECURITY",
                     "TESTING", "QBITTORRENT", "QUICKSHELL", "IPC",
                     "PACKAGING", "DEVELOPMENT", "HARNESS"]
    missing = [d for d in expected_docs
               if not (REPO / "docs/{}.md".format(d)).exists()]
    check("docs:all core files", not missing, str(missing))

    # 10. no machine-specific absolute paths / secrets / product code
    scan_files = (list((PLUGIN).rglob("*.md"))
                  + list((PLUGIN).rglob("*.json"))
                  + list((PLUGIN).rglob("*.py")))
    offenders = []
    for f in scan_files:
        text = f.read_text()
        if "/home/sysadmin" in text:
            offenders.append("{}: absolute home path".format(f.relative_to(REPO)))
        for m in SECRET_RE.finditer(text):
            offenders.append("{}: secret-like {!r}".format(
                f.relative_to(REPO), m.group(0)[:60]))
        if PLACEHOLDER_RE.search(text):
            offenders.append("{}: placeholder credential".format(f.relative_to(REPO)))
    check("harness:no machine paths / secrets / placeholders",
          not offenders, "; ".join(offenders[:5]))
    prod = [str(p.relative_to(REPO)) for p in PLUGIN.rglob("*")
            if p.suffix in (".go", ".qml", ".so")]
    check("harness:no product code", not prod, str(prod))

    fails = [r for r in results if not r[1]]
    print("\n{} checks, {} passed, {} failed".format(
        len(results), len(results) - len(fails), len(fails)))
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
