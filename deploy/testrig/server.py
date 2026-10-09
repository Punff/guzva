#!/usr/bin/env python3
"""camtest — minimal ESP32-CAM → VPS receiver for bench testing (PLAN step 0).

Deliberately NOT production: raw JPEGs ARE written to disk here so the test
page can show them. The real Go binary (PLAN §1 / D1) never writes raw.

Endpoints
  GET  /                test page (index.html, next to this file)
  GET  /status.json     last frame meta + ring index (drives the page)
  GET  /latest.jpg      newest frame
  GET  /ring/<id>.jpg   ring frames (bounded history)
  GET  /healthz         liveness
  POST /v1/ingest       proto-v1: multipart meta + frame, Bearer token
  POST /upload          raw JPEG body (curl: curl --data-binary @a.jpg https://…/upload)
  :80                   301 → https://<host>/

Config via environment (systemd EnvironmentFile /etc/camtest/camtest.env):
  CAMTEST_BIND  CAMTEST_PORT  CAMTEST_PORT80  CAMTEST_ROOT  CAMTEST_CERT
  CAMTEST_KEY   CAMTEST_TOKEN ("" = auth off)  CAMTEST_INTERVAL_S
  CAMTEST_QUALITY  CAMTEST_CFG_VER  CAMTEST_RING  CAMTEST_STALE_S
"""

import json
import os
import re
import shutil
import ssl
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import unquote, urlparse

# ---------------------------------------------------------------- config ---
BIND = os.environ.get("CAMTEST_BIND", "0.0.0.0")
PORT = int(os.environ.get("CAMTEST_PORT", "443"))
PORT80 = int(os.environ.get("CAMTEST_PORT80", "80"))
ROOT = Path(os.environ.get("CAMTEST_ROOT", "/var/lib/camtest"))
CERT = Path(os.environ.get("CAMTEST_CERT", "/etc/camtest/tls.crt"))
KEY = Path(os.environ.get("CAMTEST_KEY", "/etc/camtest/tls.key"))
TOKEN = os.environ.get("CAMTEST_TOKEN", "")
INTERVAL = int(os.environ.get("CAMTEST_INTERVAL_S", "60"))
QUALITY = int(os.environ.get("CAMTEST_QUALITY", "18"))
CFG_VER = int(os.environ.get("CAMTEST_CFG_VER", "1"))
RING = int(os.environ.get("CAMTEST_RING", "30"))
STALE_S = int(os.environ.get("CAMTEST_STALE_S", "180"))

MAX_FRAME = 4 * 1024 * 1024
MAX_META = 16 * 1024
MAX_BODY = MAX_FRAME + MAX_META + 8 * 1024
CAPTURE_ID_RE = re.compile(r"^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$")
RING_NAME_RE = re.compile(r"^[0-9]{6}\.jpg$")

# desired_cfg keys the firmware understands (PLAN §4 device plane + additive
# OV2640 sensor features). Anything else in cfg.json is ignored.
_CFG_KEYS = {
    "ver", "interval_s", "quality",
    "framesize", "brightness", "contrast", "saturation", "sharpness",
    "denoise", "special_fx", "wb_mode", "awb", "aec", "agc", "ae_level",
    "agc_gain", "hmirror", "vflip", "dcw", "lenc", "colorbar", "led_pwm",
}
JPEG_SOI = b"\xff\xd8\xff"
JPEG_EOI = b"\xff\xd9"

WWW = Path(__file__).resolve().parent
RING_DIR = ROOT / "ring"
LOG = Path(ROOT, "received.jsonl")


def now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


# ----------------------------------------------------------------- state ----
class State:
    def __init__(self):
        self.lock = threading.Lock()
        self.seq = 0
        self.last = None          # meta of the newest stored frame
        self.last_at = 0.0        # unix ts of newest stored frame
        self.history = []         # newest first: {received_at, url, size, capture_id}
        self.seen = {}            # capture_id -> received_at (idempotency, bounded)
        self.started = time.time()
        self.total = 0
        self.rejected = 0
        self.beats = 0            # health-only POSTs (no frame)

    def note_seen(self, cid: str):
        self.seen[cid] = now_iso()
        while len(self.seen) > 512:
            self.seen.pop(next(iter(self.seen)))

    def cfg(self):
        # Live bench override: ROOT/cfg.json may define any subset of
        # desired_cfg keys (ver, interval_s, quality + sensor features).
        base = {"ver": CFG_VER, "interval_s": INTERVAL, "quality": QUALITY}
        try:
            user = json.loads((ROOT / "cfg.json").read_text())
            if isinstance(user, dict):
                base.update({k: v for k, v in user.items()
                             if k in _CFG_KEYS})
        except (OSError, ValueError):
            pass
        return base

    def resp_body(self):
        return {
            "desired_cfg": self.cfg(),
            "next_s": INTERVAL,
            "server_time": now_iso(),
        }


S = State()


def store_frame(data: bytes, meta: dict) -> dict:
    """Atomic write into the ring + latest.jpg. Caller holds no lock."""
    with S.lock:
        S.seq += 1
        seq = S.seq
    ring_name = f"{seq:06d}.jpg"
    tmp = RING_DIR / (ring_name + ".tmp")
    tmp.write_bytes(data)
    os.replace(tmp, RING_DIR / ring_name)
    tmp = ROOT / "latest.jpg.tmp"
    tmp.write_bytes(data)
    os.replace(tmp, ROOT / "latest.jpg")

    entry = {
        "received_at": now_iso(),
        "url": f"/ring/{ring_name}",
        "size": len(data),
        "capture_id": meta.get("capture_id", ""),
        "fw": meta.get("fw", ""),
        "rssi": meta.get("rssi"),
        "free_heap": meta.get("free_heap"),
        "reason": meta.get("reason", ""),
        "counters": meta.get("counters", {}),
    }
    with S.lock:
        S.last = entry
        S.last_at = time.time()
        S.history.insert(0, entry)
        del S.history[RING:]
    # prune old ring files
    files = sorted(RING_DIR.glob("*.jpg"))
    for old in files[:-RING]:
        try:
            old.unlink()
        except OSError:
            pass
    try:
        with LOG.open("a") as f:
            f.write(json.dumps(entry, separators=(",", ":")) + "\n")
    except OSError:
        pass
    return entry


# ------------------------------------------------------------- validation ---
class Reject(Exception):
    def __init__(self, code: int, reason: str):
        super().__init__(reason)
        self.code = code
        self.reason = reason


def validate_frame(data: bytes):
    if not data:
        raise Reject(422, "empty_frame")
    if len(data) > MAX_FRAME:
        raise Reject(413, "too_big")
    if not data.startswith(JPEG_SOI):
        raise Reject(422, "bad_magic")
    if not data.endswith(JPEG_EOI):
        raise Reject(422, "truncated")


def validate_meta(meta: dict) -> str:
    cid = meta.get("capture_id", "")
    if not CAPTURE_ID_RE.match(cid):
        raise Reject(422, "bad_capture_id")
    return cid


def parse_multipart(body: bytes, ctype: str) -> dict:
    m = re.search(r'boundary="?([^";]+)"?', ctype)
    if not m:
        raise Reject(422, "bad_meta")
    delim = b"--" + m.group(1).encode()
    out = {}
    for chunk in body.split(delim):
        chunk = chunk.strip(b"\r\n")
        if not chunk:
            continue
        if chunk.startswith(b"--"):
            break
        if b"\r\n\r\n" not in chunk:
            continue
        hdr, _, data = chunk.partition(b"\r\n\r\n")
        nm = re.search(rb'name="([^"]+)"', hdr, re.I)
        if nm:
            out[nm.group(1).decode("ascii", "replace")] = data
    return out


# ---------------------------------------------------------------- handlers --
class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "camtest"

    def log_message(self, fmt, *args):
        sys.stderr.write("%s %s\n" % (time.strftime("%H:%M:%S"), fmt % args))

    # -------------------------------------------------------------- helpers --
    def send_json(self, code: int, obj: dict, extra: dict | None = None):
        body = json.dumps(obj, separators=(",", ":")).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Proto", "v1")
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def send_file(self, path: Path, ctype: str, cache: str = "no-store"):
        try:
            data = path.read_bytes()
        except OSError:
            self.send_json(404, {"error": "not_found"})
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", cache)
        self.end_headers()
        self.wfile.write(data)

    def send_err_json(self, code: int, reason: str, extra: dict | None = None):
        obj = {"error": reason}
        obj.update(extra or {})
        self.send_json(code, obj)

    def read_body(self) -> bytes:
        n = int(self.headers.get("Content-Length") or 0)
        if n <= 0:
            raise Reject(422, "no_body")
        if n > MAX_BODY:
            self.close_connection = True
            raise Reject(413, "too_big")
        return self.rfile.read(n)

    def check_auth(self):
        if not TOKEN:
            return
        got = self.headers.get("Authorization", "")
        if got != f"Bearer {TOKEN}":
            self.close_connection = True  # request body not consumed
            raise Reject(401, "unauthorized")

    # ----------------------------------------------------------------- GET --
    def do_GET(self):
        path = urlparse(self.path).path
        try:
            if path in ("/", "/index.html"):
                self.send_file(WWW / "index.html", "text/html; charset=utf-8")
            elif path == "/status.json":
                with S.lock:
                    fresh = bool(S.last) and (time.time() - S.last_at) < STALE_S
                    obj = {
                        "ok": True,
                        "server_time": now_iso(),
                        "uptime_s": int(time.time() - S.started),
                        "fresh": fresh,
                        "stale_after_s": STALE_S,
                        "frames_total": S.total,
                        "rejected": S.rejected,
                        "health_beats": S.beats,
                        "cfg": S.cfg(),
                        "last": S.last,
                        "history": S.history[:12],
                    }
                self.send_json(200, obj)
            elif path == "/latest.jpg":
                if not (ROOT / "latest.jpg").exists():
                    self.send_json(404, {"error": "no_frame_yet"})
                    return
                self.send_file(ROOT / "latest.jpg", "image/jpeg", "no-store")
            elif path.startswith("/ring/"):
                name = unquote(path[len("/ring/"):])
                if not RING_NAME_RE.match(name):
                    self.send_json(404, {"error": "not_found"})
                    return
                self.send_file(RING_DIR / name, "image/jpeg", "public, max-age=60")
            elif path == "/healthz":
                du = shutil.disk_usage(ROOT)
                with S.lock:
                    obj = {
                        "ok": True,
                        "version": "camtest-1",
                        "uptime_s": int(time.time() - S.started),
                        "disk_free_pct": round(100 * du.free / du.total, 1),
                        "frames_total": S.total,
                    }
                self.send_json(200, obj)
            else:
                self.send_json(404, {"error": "not_found", "hint": "/"})
        except BrokenPipeError:
            pass

    # ---------------------------------------------------------------- POST --
    def do_POST(self):
        try:
            path = urlparse(self.path).path
            if path == "/v1/ingest":
                self.handle_ingest()
            elif path == "/upload":
                self.handle_upload()
            else:
                self.send_err_json(404, "not_found")
        except Reject as r:
            with S.lock:
                S.rejected += 1
            extra = {}
            if r.code in (401, 422):
                extra = {"reason": r.reason}
            if r.code == 401:
                self.send_err_json(401, "unauthorized")
            elif r.code == 422:
                body = {"reason": r.reason}
                body.update(S.resp_body())
                self.send_json(422, body)
            else:
                self.send_err_json(r.code, r.reason, extra)
        except BrokenPipeError:
            pass

    def handle_ingest(self):
        self.check_auth()
        ctype = self.headers.get("Content-Type", "")
        if "multipart/form-data" not in ctype:
            raise Reject(422, "bad_meta")
        body = self.read_body()
        parts = parse_multipart(body, ctype)

        raw_meta = parts.get("meta")
        if not raw_meta or len(raw_meta) > MAX_META:
            raise Reject(422, "bad_meta")
        try:
            meta = json.loads(raw_meta.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            raise Reject(422, "bad_meta")
        cid = validate_meta(meta)

        frame = parts.get("frame")
        if frame:
            validate_frame(frame)

        with S.lock:
            duplicate = cid in S.seen
        if duplicate:
            # idempotent retry: same body as 201, frame not re-stored
            self.send_json(409, S.resp_body())
            return

        if frame:
            store_frame(frame, meta)
        with S.lock:
            S.total += 1
            if not frame:
                S.beats += 1
            S.note_seen(cid)
        self.send_json(201, S.resp_body())

    def handle_upload(self):
        """Raw JPEG convenience endpoint (curl), no multipart, meta synthesized."""
        self.check_auth()
        ctype = self.headers.get("Content-Type", "")
        data = self.read_body()
        if "multipart/form-data" in ctype:
            parts = parse_multipart(data, ctype)
            data = parts.get("frame") or data
        validate_frame(data)
        cid = self.headers.get("X-Capture-Id") or f"C0-{int(time.time()) % 100000}-" \
              + f"{int(time.time()) & 0xffff:04x}"
        if not CAPTURE_ID_RE.match(cid):
            raise Reject(422, "bad_capture_id")
        with S.lock:
            if cid in S.seen:
                self.send_json(409, S.resp_body())
                return
        meta = {
            "capture_id": cid,
            "fw": self.headers.get("X-Fw", "curl"),
            "rssi": None,
            "free_heap": None,
            "reason": "",
        }
        store_frame(data, meta)
        with S.lock:
            S.total += 1
            S.note_seen(cid)
        self.send_json(201, S.resp_body())


class RedirectHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def _go(self):
        host = (self.headers.get("Host") or "").split(":")[0] or "localhost"
        body = b"redirecting to https\n"
        self.send_response(301)
        self.send_header("Location", f"https://{host}/")
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = _go
    do_POST = _go
    do_HEAD = _go


def main():
    RING_DIR.mkdir(parents=True, exist_ok=True)
    ROOT.mkdir(parents=True, exist_ok=True)

    if not CERT.exists() or not KEY.exists():
        sys.exit(f"missing TLS cert/key: {CERT} {KEY}")

    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    ctx.load_cert_chain(str(CERT), str(KEY))

    https = ThreadingHTTPServer((BIND, PORT), Handler)
    https.daemon_threads = True
    https.socket = ctx.wrap_socket(https.socket, server_side=True)

    if PORT80:
        plain = ThreadingHTTPServer((BIND, PORT80), RedirectHandler)
        plain.daemon_threads = True
        threading.Thread(target=plain.serve_forever, daemon=True).start()

    print(f"camtest: https://{BIND}:{PORT} root={ROOT} "
          f"auth={'on' if TOKEN else 'off'} ring={RING} interval={INTERVAL}s",
          flush=True)
    try:
        https.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
