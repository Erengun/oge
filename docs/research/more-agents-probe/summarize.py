#!/usr/bin/env python3
"""Condense an acp_probe.py log: merge streamed text chunks, shorten long fields."""
import json, re, sys

def short(v, n=160):
    s = json.dumps(v, ensure_ascii=False)
    return s if len(s) <= n else s[:n] + "…"

buf_kind, buf_text, buf_t = None, "", ""
def flush():
    global buf_kind, buf_text
    if buf_kind:
        print(f"{buf_t} < update {buf_kind}: {short(buf_text, 200)}")
    buf_kind, buf_text = None, ""

for line in open(sys.argv[1]):
    m = re.match(r"\s*([\d.]+) ([<>]) (.*)", line)
    if not m:
        flush(); print(line.rstrip()); continue
    t, d, body = m.groups()
    msg = json.loads(body)
    up = msg.get("params", {}).get("update") if msg.get("method") == "session/update" else None
    if up and up["sessionUpdate"] in ("agent_message_chunk", "agent_thought_chunk"):
        if buf_kind != up["sessionUpdate"]:
            flush(); buf_kind, buf_t = up["sessionUpdate"], t
        buf_text += up.get("content", {}).get("text", "")
        continue
    flush()
    if up:
        k = up["sessionUpdate"]
        if k == "available_commands_update":
            print(f"{t} < update {k}: {len(up['availableCommands'])} commands"); continue
        rest = {x: up[x] for x in up if x not in ("sessionUpdate",)}
        print(f"{t} < update {k}: {short(rest, 400)}")
    elif msg.get("method"):
        print(f"{t} {d} {msg['method']} id={msg.get('id')}: {short(msg.get('params'), 600)}")
    else:
        print(f"{t} {d} response id={msg.get('id')}: {short(msg.get('result', msg.get('error')), 300)}")
flush()
