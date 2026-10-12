#!/usr/bin/env python3
"""Minimal ACP v1 stdio client used for the OpenCode live probe (#136).

Usage:
  acp_probe.py --cwd DIR [--prompt TEXT] [--decide allow,deny,...] [--timeout S] -- AGENT ARGV...

Sends initialize -> session/new (-> session/prompt), prints every JSON-RPC
message as one line ("> " sent, "< " received). Answers
session/request_permission from the --decide list in order (allow =
first allow_once option, deny = first reject_once option; default deny once
the list runs out). Never calls `authenticate`. Advertises no fs/terminal
capabilities, so the agent uses its own tools.
"""
import argparse, json, os, subprocess, sys, threading, time, queue

ap = argparse.ArgumentParser()
ap.add_argument("--cwd", required=True)
ap.add_argument("--prompt")
ap.add_argument("--decide", default="")
ap.add_argument("--timeout", type=float, default=300)
ap.add_argument("agent", nargs=argparse.REMAINDER)
a = ap.parse_args()
argv = a.agent[1:] if a.agent and a.agent[0] == "--" else a.agent
decisions = [d for d in a.decide.split(",") if d]

p = subprocess.Popen(argv, cwd=a.cwd, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                     stderr=open(os.environ.get("PROBE_STDERR", os.devnull), "w"), text=True, bufsize=1)
inbox = queue.Queue()
t0 = time.time()

def log(prefix, msg):
    print(f"{time.time()-t0:7.2f} {prefix} {json.dumps(msg, ensure_ascii=False)}", flush=True)

def reader():
    for line in p.stdout:
        line = line.strip()
        if not line:
            continue
        try:
            inbox.put(json.loads(line))
        except ValueError:
            print(f"        ! non-JSON stdout: {line[:200]}", flush=True)
    inbox.put(None)

threading.Thread(target=reader, daemon=True).start()
next_id = [0]

def send(obj):
    log(">", obj)
    p.stdin.write(json.dumps(obj) + "\n"); p.stdin.flush()

def request(method, params):
    next_id[0] += 1
    send({"jsonrpc": "2.0", "id": next_id[0], "method": method, "params": params})
    return next_id[0]

def handle_agent_request(m):
    if m["method"] == "session/request_permission":
        want = decisions.pop(0) if decisions else "deny"
        kind = "allow_once" if want == "allow" else "reject_once"
        opts = m["params"].get("options", [])
        pick = next((o for o in opts if o.get("kind") == kind), None)
        if pick is None:
            res = {"outcome": {"outcome": "cancelled"}}
        else:
            res = {"outcome": {"outcome": "selected", "optionId": pick["optionId"]}}
        send({"jsonrpc": "2.0", "id": m["id"], "result": res})
    else:
        send({"jsonrpc": "2.0", "id": m["id"], "error": {"code": -32601, "message": "not supported by probe"}})

def wait_for(rid):
    deadline = t0 + a.timeout
    while True:
        try:
            m = inbox.get(timeout=max(0.1, deadline - time.time()))
        except queue.Empty:
            print("        ! timeout", flush=True); return None
        if m is None:
            print(f"        ! agent exited rc={p.poll()}", flush=True); return None
        log("<", m)
        if "method" in m and "id" in m:
            handle_agent_request(m)
        elif m.get("id") == rid and "method" not in m:
            return m

try:
    r = wait_for(request("initialize", {
        "protocolVersion": 1,
        "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False},
        "clientInfo": {"name": "oge-probe", "version": "0"}}))
    if r and "result" in r:
        r = wait_for(request("session/new", {"cwd": a.cwd, "mcpServers": []}))
    if r and "result" in r and a.prompt:
        sid = r["result"]["sessionId"]
        wait_for(request("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": a.prompt}]}))
finally:
    p.terminate()
    try:
        p.wait(5)
    except subprocess.TimeoutExpired:
        p.kill()
