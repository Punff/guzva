/*
 * ============================================================================
 *  imaliguzve — lean ESP32-CAM capture firmware
 *  PLAN §5 "dumb sensor, smart about survival" · Phase 1 · proto-v1.md
 *  AI-Thinker ESP32-CAM / OV2640 · PlatformIO + arduino-esp32
 * ----------------------------------------------------------------------------
 *  Does (build steps 1-7 of PLAN §5):
 *    - capture a JPEG on a timer (interval driven by server desired_cfg)
 *    - hand-built multipart POST to https://<INGEST_HOST>/v1/ingest
 *      (part "meta" JSON + optional part "frame", per docs/proto-v1.md)
 *    - apply desired_cfg (interval_s 30-300 / quality / ver), echo
 *      cfg_ver_applied on the next POST, sleep next_s from the response
 *    - no frame queue: upload failure = drop frame, post_fail++, fresh
 *      capture on the next tick
 *    - TLS: WiFiClientSecure + embedded CA (setCACert); keep-alive reuse of
 *      one client across ticks; 8 s connect / 25 s POST budgets
 *    - non-blocking WiFi manager (WiFi.begin once, backoff retries from
 *      loop(); capture never waits for WiFi, there is no NTP in v1)
 *    - camera failure => exponential backoff + health-only beats, never a
 *      hot-reboot loop
 *    - NVS identity in "prov", camera config in "cam", factory reset over
 *      USB serial ("reset") and the caged LAN endpoint
 *    - LAN cage on port 80: GET /api/status, GET /jpg (1 per 3 s),
 *      GET /ping, POST /api/factory-reset?confirm=1 — nothing else
 *    - mDNS imaliguzve-<serial>.local, serial from efuse MAC
 *    - status LED GPIO33 (active LOW), all patterns millis()-based
 *
 *  Deliberately NOT here (PLAN §5 "prod builds delete"): port-81 MJPEG
 *  stream, motion, timelapse, SD/SPIFFS photo storage, GPIO switchboard,
 *  OTA upload form, RGB/grayscale endpoints, the big web dashboard, NTP.
 *
 *  Counters: boot + brownout persist in NVS ("prov"); join_fail / post_fail /
 *  tls_fail / capture_fail live in RAM and reset each boot — intentional
 *  (PLAN §5 item 7: RAM is fine, documented here).
 * ============================================================================
 */

// ============================ INCLUDES =====================================
#include <Arduino.h>
#include <WiFi.h>
#include <WiFiClientSecure.h>
#include <WebServer.h>
#include <ESPmDNS.h>
#include <Preferences.h>
#include <esp_camera.h>
#include <esp_system.h>
#include <esp_random.h>
#include <esp_heap_caps.h>

#include "wifiSettings.h"

// TLS escape hatch — the production value (-DINSECURE_TLS=0) lives in
// platformio.ini build_flags and is grep-gated: prod builds must show
// INSECURE_TLS=0 and no reachable setInsecure() outside this guard.
#ifndef INSECURE_TLS
#define INSECURE_TLS 0
#endif

// Firmware semver — reported in every meta "fw" field (proto-v1.md).
#define FW_VERSION "1.0.0"

// ============================ HARDWARE PINS ================================
// AI-Thinker ESP32-CAM camera wiring. These GPIOs are fixed by the board —
// not general-purpose pins. (Copied from the reference sketch src/main.cpp.)
#define PWDN_GPIO_NUM     32
#define RESET_GPIO_NUM    -1
#define XCLK_GPIO_NUM      0
#define SIOD_GPIO_NUM     26
#define SIOC_GPIO_NUM     27
#define Y9_GPIO_NUM       35
#define Y8_GPIO_NUM       34
#define Y7_GPIO_NUM       39
#define Y6_GPIO_NUM       36
#define Y5_GPIO_NUM       21
#define Y4_GPIO_NUM       19
#define Y3_GPIO_NUM       18
#define Y2_GPIO_NUM        5
#define VSYNC_GPIO_NUM    25
#define HREF_GPIO_NUM     23
#define PCLK_GPIO_NUM     22

#define FLASH_GPIO_NUM     4        // white flash LED (unused in v1)
#define INDICATOR_LED_PIN  33       // status LED, active LOW

// ============================ LIMITS & TUNABLES ============================
static const uint32_t WIFI_RETRY_MIN_MS   = 10000;   // join backoff 10 s .. 60 s
static const uint32_t WIFI_RETRY_MAX_MS   = 60000;
// Bench-bench measured: lwIP RTO ≈ 3 s on the weak link, so SYN retransmits at
// 0/3/9 s and the ClientHello retransmit at 9 s. The old 8 s budget cut the
// connection off BEFORE the 9 s retransmit could go out — server logs matched
// exactly (accepts at ~3 s TCP + 8 s HS, all ending 8.2 s / 11.6 s). 12 s
// covers the 9 s retransmit; server ReadHeaderTimeout=15 s still outlasts it
// (PLAN §5 8 s deviated — justified by 2026-10-08 bench data, ops-log).
static const uint32_t TLS_HANDSHAKE_S     = 12;      // cover the 9 s CH retransmit
static const uint32_t TLS_CONNECT_MS      = 12000;   // TCP phase: SYN at 0/3/9 s
static const uint32_t POST_TIMEOUT_MS     = 25000;   // PLAN: 25 s POST
static const uint8_t  POST_ATTEMPTS       = 3;       // transport retries/tick
static const uint32_t POST_RETRY_GAP_MS   = 6000;    // decorrelate interference bursts
static const uint32_t KEEPALIVE_IDLE_MS   = 45000;   // well under Caddy 60 s+ idle
static const uint32_t JPG_RATE_MS         = 3000;    // /jpg: 1 per 3 s
static const size_t   UPLOAD_CHUNK        = 6144;    // 6 KiB chunks (spec 4-8 KiB)
static const uint32_t INTERVAL_MIN_S      = 30;      // proto-v1 range
static const uint32_t INTERVAL_MAX_S      = 300;
static const uint32_t REVOKED_BACKOFF_MIN = 60;      // 401: 60 -> 600 s (proto)
static const uint32_t REVOKED_BACKOFF_MAX = 600;
static const uint32_t CAM_BACKOFF_MIN_S   = 10;      // camera probe backoff
static const uint32_t CAM_BACKOFF_MAX_S   = 1800;
static const uint8_t  CAM_FAIL_LIMIT      = 5;       // degraded after 5 (PLAN §5)

// ============================ MULTIPART (proto-v1) =========================
// Hand-built multipart: meta part first, optional frame part, closing
// boundary. Built at compile time — no String concatenation on the hot path.
#define MP_BOUNDARY "imaliguzve-v1-9f3a"
static const char PART_META_HDR[] =
    "--" MP_BOUNDARY "\r\n"
    "Content-Disposition: form-data; name=\"meta\"\r\n"
    "Content-Type: application/json\r\n\r\n";
static const char PART_FRAME_HDR[] =
    "--" MP_BOUNDARY "\r\n"
    "Content-Disposition: form-data; name=\"frame\"; filename=\"frame.jpg\"\r\n"
    "Content-Type: image/jpeg\r\n\r\n";
static const char PART_TAIL[] = "--" MP_BOUNDARY "--\r\n";

// ============================ STATE ========================================
// --- NVS-backed -------------------------------------------------------------
//   namespace "prov": boot, brownout, capture (id counter), cfgver, token
//   namespace "cam" : interval (s), quality
Preferences provPrefs, camPrefs;

uint32_t bootCount      = 0;        // prov "boot"     (persisted, ++ per boot)
uint32_t brownoutCount  = 0;        // prov "brownout" (persisted)
uint32_t captureSeq     = 0;        // prov "capture"  (persisted, monotonic)
uint32_t cfgVerApplied  = 0;        // prov "cfgver"   (read-back per proto)
char     deviceToken[80] = {0};     // prov "token", fallback wifiSettings.h
uint32_t intervalS      = 60;       // cam "interval"  (server desired_cfg)
uint8_t  jpegQuality    = 14;       // cam "quality"   (server desired_cfg)

// --- RAM counters (reset on boot; documented in file header) ----------------
uint32_t joinFail = 0, postFail = 0, tlsFail = 0, captureFail = 0;

// --- runtime ----------------------------------------------------------------
char     serialNo[8]     = {0};     // efuse MAC low 3 bytes, "a1b2c3"
char     mdnsName[40]    = {0};     // imaliguzve-<serial>
char     captureId[40]   = {0};     // C<boot>-<ctr>-<rand4>
char     metaBuf[1024]   = {0};     // meta JSON, built per tick
char     reqHead[640]    = {0};     // HTTP request head (fixed buffers only)
char     respHdr[1024]   = {0};     // HTTP response headers
char     respBody[1024]  = {0};     // HTTP response body (small JSON)

WiFiClientSecure tlsClient;         // ONE client, reused across ticks
WebServer server(80);               // LAN cage (installer-facing only)

uint32_t tlsLastUseMs    = 0;       // keep-alive bookkeeping
bool     camInited       = false;   // esp_camera_init succeeded at least now
bool     camEverOk       = false;   // a frame was captured at least once
bool     camDegraded     = false;   // health-only beats, camera in backoff
uint8_t  camFailStreak   = 0;
uint32_t camBackoffS     = 0;       // current camera retry backoff
uint32_t camRetryAt      = 0;       // millis() when camera may be retried

bool     revoked401      = false;   // server said 401: slow health-only beats
uint32_t revokedBackoffS = REVOKED_BACKOFF_MIN;

uint32_t nextTickAt      = 0;       // millis() of the next ingest tick
int      lastHttp        = 0;       // last HTTP status (0 = transport fail)
uint32_t ledOkFlashUntil = 0;       // brief LED flash after a good POST
uint32_t pendingFactoryAt = 0;      // scheduled factory reset (0 = none)
uint32_t wifiLastAttempt = 0;
uint32_t wifiRetryMs     = WIFI_RETRY_MIN_MS;
bool     wifiWasDown     = true;

// ============================ SMALL HELPERS ================================
// millis() comparison that survives the 49-day wrap.
static bool due(uint32_t at) { return (int32_t)(millis() - at) >= 0; }

// Tiny JSON reader: find `"key":` and parse the unsigned decimal that
// follows (whitespace tolerated). No JSON library — proto responses are
// tiny and only these few fields matter.
static bool jsonU32(const char *body, const char *key, uint32_t *out) {
  const char *p = strstr(body, key);
  if (!p) return false;
  p += strlen(key);
  while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') p++;
  if (*p < '0' || *p > '9') return false;
  char *end = nullptr;
  unsigned long v = strtoul(p, &end, 10);
  if (end == p) return false;
  *out = (uint32_t)v;
  return true;
}

// Tiny JSON string reader: find `"key":"` and copy until the closing quote.
static bool jsonStr(const char *body, const char *key, char *out, size_t cap) {
  const char *p = strstr(body, key);
  if (!p) { out[0] = 0; return false; }
  p += strlen(key);
  const char *q = strchr(p, '"');
  if (!q) { out[0] = 0; return false; }
  size_t n = (size_t)(q - p);
  if (n >= cap) n = cap - 1;
  memcpy(out, p, n);
  out[n] = 0;
  return true;
}

// ============================ LED (GPIO33, active LOW) ======================
// Patterns are millis()-based state, never delay():
//   connecting = 2 Hz blink · ok = short flash after a good POST
//   revoked401 = double-blink per 1.2 s · camera-degraded = slow 0.5 Hz pulse
static void ledWrite(bool on) { digitalWrite(INDICATOR_LED_PIN, on ? LOW : HIGH); }
static void ledOkFlash()      { ledOkFlashUntil = millis() + 150; }

static void ledService() {
  uint32_t now = millis();
  if (ledOkFlashUntil != 0 && !due(ledOkFlashUntil)) { ledWrite(true); return; }
  if (revoked401) {                       // revoked: amber-style double blink
    uint32_t ph = now % 1200;
    ledWrite(ph < 90 || (ph >= 200 && ph < 290));
  } else if (camDegraded) {               // camera degraded: slow pulse
    ledWrite((now % 2000) < 1000);
  } else if (WiFi.status() != WL_CONNECTED) {  // connecting: 2 Hz blink
    ledWrite((now % 500) < 250);
  } else {
    ledWrite(false);
  }
}

// ============================ NVS / IDENTITY ===============================
// Load (and roll forward) everything that must survive a reboot. Namespace
// "prov" = identity + monotonic counters; namespace "cam" = device-plane
// config. Called once from setup(); the Preferences handles stay open.
static void loadNvs() {
  provPrefs.begin("prov", false);
  camPrefs.begin("cam", false);

  bootCount = provPrefs.getULong("boot", 0) + 1;
  provPrefs.putULong("boot", bootCount);            // monotonic boot counter

  brownoutCount = provPrefs.getULong("brownout", 0);
  if (esp_reset_reason() == ESP_RST_BROWNOUT) {
    brownoutCount++;
    provPrefs.putULong("brownout", brownoutCount);
    Serial.println("[NVS] brownout reset counted");
  }

  captureSeq    = provPrefs.getULong("capture", 0);
  cfgVerApplied = provPrefs.getULong("cfgver", 0);
  String tok = provPrefs.getString("token", DEVICE_TOKEN);
  snprintf(deviceToken, sizeof deviceToken, "%s", tok.c_str());

  uint32_t iv = camPrefs.getULong("interval", 60);
  intervalS   = (uint32_t)constrain(iv, INTERVAL_MIN_S, INTERVAL_MAX_S);
  uint32_t q  = camPrefs.getUChar("quality", 14);
  jpegQuality = (uint8_t)constrain(q, (uint32_t)5, (uint32_t)63);
}

// Factory reset: clears identity ("prov") and config ("cam"), then restarts.
// Entry points: serial command "reset" and POST /api/factory-reset?confirm=1.
static void factoryReset() {
  Serial.println("[NVS] factory reset: clearing 'prov' + 'cam'");
  provPrefs.clear();
  camPrefs.clear();
  Serial.println("[NVS] cleared, restarting");
  delay(100);                                        // let Serial drain
  ESP.restart();
}

// capture_id = "C<boot>-<ctr>-<rand4>" — matches proto
// ^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$ ; counter monotonic in NVS,
// rand4 = 4 lowercase hex from esp_random. Issued on every tick (frames and
// health-only beats alike) so it doubles as the idempotency key.
static void nextCaptureId() {
  captureSeq++;
  provPrefs.putULong("capture", captureSeq);
  uint32_t r = esp_random() & 0xFFFF;
  snprintf(captureId, sizeof captureId, "C%lu-%lu-%04lx",
           (unsigned long)bootCount, (unsigned long)captureSeq,
           (unsigned long)r);
}

// ============================ CAMERA =======================================
// Build the driver config for the fixed AI-Thinker wiring. PSRAM-aware:
// frames live in PSRAM, latest-frame grab, 2 frame buffers.
static bool cameraTryInit(uint32_t xclkHz) {
  camera_config_t c = {};
  c.ledc_channel = LEDC_CHANNEL_0;
  c.ledc_timer   = LEDC_TIMER_0;
  c.pin_d0       = Y2_GPIO_NUM;
  c.pin_d1       = Y3_GPIO_NUM;
  c.pin_d2       = Y4_GPIO_NUM;
  c.pin_d3       = Y5_GPIO_NUM;
  c.pin_d4       = Y6_GPIO_NUM;
  c.pin_d5       = Y7_GPIO_NUM;
  c.pin_d6       = Y8_GPIO_NUM;
  c.pin_d7       = Y9_GPIO_NUM;
  c.pin_xclk     = XCLK_GPIO_NUM;
  c.pin_pclk     = PCLK_GPIO_NUM;
  c.pin_vsync    = VSYNC_GPIO_NUM;
  c.pin_href     = HREF_GPIO_NUM;
  c.pin_sccb_sda = SIOD_GPIO_NUM;
  c.pin_sccb_scl = SIOC_GPIO_NUM;
  c.pin_pwdn     = PWDN_GPIO_NUM;
  c.pin_reset    = RESET_GPIO_NUM;
  c.xclk_freq_hz = xclkHz;
  c.pixel_format = PIXFORMAT_JPEG;
  c.frame_size   = psramFound() ? FRAMESIZE_VGA : FRAMESIZE_QVGA;
  c.jpeg_quality = jpegQuality;
  c.fb_location  = psramFound() ? CAMERA_FB_IN_PSRAM : CAMERA_FB_IN_DRAM;
  c.grab_mode    = CAMERA_GRAB_LATEST;
  c.fb_count     = psramFound() ? 2 : 1;

  esp_err_t err = esp_camera_init(&c);
  if (err != ESP_OK) {
    Serial.printf("[CAM] init failed 0x%x (xclk %lu MHz)\n", err,
                  (unsigned long)(xclkHz / 1000000UL));
    return false;
  }
  // Re-apply device-plane quality (NVS) — init only set the boot default.
  sensor_t *s = esp_camera_sensor_get();
  if (s) s->set_quality(s, jpegQuality);
  return true;
}

// 20 MHz XCLK first, 16 MHz fallback on failure (PLAN §5 keeps the fallback).
static bool initCamera() {
  if (cameraTryInit(20000000)) return true;
  Serial.println("[CAM] 20 MHz XCLK failed -> 16 MHz fallback");
  return cameraTryInit(16000000);
}

// Enter (or refresh) camera-degraded mode: health-only beats keep the server
// liveness signal, the camera gets an exponentially growing retry backoff.
// Never a hot-reboot loop.
static void camEnterDegraded() {
  camDegraded = true;
  camBackoffS = camBackoffS ? camBackoffS * 2 : CAM_BACKOFF_MIN_S;
  if (camBackoffS > CAM_BACKOFF_MAX_S) camBackoffS = CAM_BACKOFF_MAX_S;
  camRetryAt = millis() + camBackoffS * 1000UL;
  Serial.printf("[CAM] degraded: health-only beats, camera retry in %lu s\n",
                (unsigned long)camBackoffS);
}

// ============================ WIFI MANAGER =================================
// WiFi.begin exactly once; reconnects are retried from loop() with backoff.
// There is never a delay() waiting for WiFi — a due ingest tick simply waits
// at the gate until the link is back (no queue, no stale frames).
// WiFi events: exact join diagnosis on serial. Reason codes (§wifi_err_reason):
// 200/201 = AP not found (RF/position), 202 = auth failed (password),
// 203 = assoc rejected, 15 = 4-way handshake timeout.
static bool wifiScanOnce = false;  // one visibility scan per boot (retry path)
static bool wifiEverUp = false;    // "dropped" vs "never connected" in logs

static void wifiEvent(arduino_event_id_t id, arduino_event_info_t info) {
  switch (id) {
    case ARDUINO_EVENT_WIFI_STA_DISCONNECTED:
      Serial.printf("[WiFi] disconnected reason=%u\n",
                    info.wifi_sta_disconnected.reason);
      break;
    case ARDUINO_EVENT_WIFI_STA_GOT_IP:
      Serial.printf("[WiFi] got IP %s rssi=%d\n",
                    WiFi.localIP().toString().c_str(),
                    (int)WiFi.RSSI());
      break;
    default:
      break;
  }
}

// Visibility scan: what does the board hear? Runs once per boot, from the
// retry path (radio idle after disconnect — scanning mid-join fails: -2).
static void wifiScanReport() {
  int n = WiFi.scanNetworks(/*async=*/false, /*show_hidden=*/true);
  Serial.printf("[WiFi] scan: %d networks%s\n", n,
                n == 0 ? " (board hears NONE)" : "");
  for (int i = 0; i < n && i < 15; i++) {
    Serial.printf("  \"%s\" ch=%d rssi=%d %s\n", WiFi.SSID(i).c_str(),
                  (int)WiFi.channel(i), (int)WiFi.RSSI(i),
                  WiFi.encryptionType(i) == WIFI_AUTH_OPEN ? "open" : "sec");
  }
  WiFi.scanDelete();
}

static void startWiFi() {
  WiFi.onEvent(wifiEvent);
  WiFi.mode(WIFI_STA);
  WiFi.setHostname(mdnsName);
  WiFi.setSleep(false);                    // reliability over power saving
  WiFi.setTxPower(WIFI_POWER_19_5dBm);     // bench: weak-link headroom
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  wifiLastAttempt = millis();
  wifiWasDown = false;
}

static void serviceWiFi() {
  if (WiFi.status() == WL_CONNECTED) {
    wifiRetryMs = WIFI_RETRY_MIN_MS;
    wifiWasDown = false;
    wifiEverUp = true;
    return;
  }
  if (!wifiWasDown) {                      // link just dropped: note it
    wifiWasDown = true;
    wifiLastAttempt = millis();
    tlsClient.stop();                      // stale TLS session is useless
    if (wifiEverUp)                        // first boot has no "link" to lose
      Serial.println("[WiFi] link lost, retry with backoff");
    return;
  }
  if (millis() - wifiLastAttempt < wifiRetryMs) return;
  joinFail++;                              // previous join attempt failed
  wifiLastAttempt = millis();
  wifiRetryMs = wifiRetryMs * 2 > WIFI_RETRY_MAX_MS ? WIFI_RETRY_MAX_MS
                                                    : wifiRetryMs * 2;
  WiFi.disconnect();
  if (!wifiScanOnce) { wifiScanOnce = true; wifiScanReport(); }
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  Serial.printf("[WiFi] retry (join_fail=%lu, status=%d, backoff %lu ms)\n",
                (unsigned long)joinFail, (int)WiFi.status(),
                (unsigned long)wifiRetryMs);
}

// ============================ TLS TRANSPORT ================================
// Reuse the single WiFiClientSecure across ticks while it is still up and
// its idle window is comfortably under Caddy's 60 s+ idle timeout; otherwise
// reconnect (measured — the number feeds the PLAN §5 "early spike").
static bool tlsEnsureConnected(bool *reused, uint32_t *connectMs) {
  *reused = false;
  *connectMs = 0;
  uint32_t now = millis();
  if (tlsClient.connected() && (now - tlsLastUseMs) < KEEPALIVE_IDLE_MS) {
    *reused = true;
    return true;
  }
  if (tlsClient.connected()) tlsClient.stop();   // idle too long: fresh TLS

#if INSECURE_TLS
  tlsClient.setInsecure();  // DEV ONLY — prod builds grep-gate this behind
                            // INSECURE_TLS=0 (see platformio.ini build_flags)
#else
  tlsClient.setCACert(CA_ROOT_PEM);
#endif
  uint32_t t0 = millis();
  bool ok = tlsClient.connect(INGEST_HOST, INGEST_PORT,
                              (int32_t)TLS_CONNECT_MS) != 0;
  *connectMs = millis() - t0;
  if (!ok) {
    tlsClient.stop();
    return false;
  }
  tlsClient.setTimeout(POST_TIMEOUT_MS / 1000);   // seconds: socket r/w budget
  return true;
}

// Write exactly len bytes before the (25 s) POST deadline.
static bool tlsWriteAll(const uint8_t *p, size_t len, uint32_t deadline) {
  size_t off = 0;
  while (off < len) {
    if (due(deadline)) return false;
    size_t w = tlsClient.write(p + off, len - off);
    if (w == 0) return false;
    off += w;
  }
  return true;
}

// Core reader: exactly n bytes; stored into buf[*stored .. cap-2] while they
// fit, the rest drained so the keep-alive connection stays usable.
static bool tlsReadInto(size_t n, char *buf, size_t cap, size_t *stored,
                        uint32_t deadline) {
  size_t got = 0;
  while (got < n) {
    if (due(deadline)) return false;
    int avail = tlsClient.available();
    if (avail > 0) {
      size_t want = n - got;
      if (want > 256) want = 256;
      if ((size_t)avail < want) want = (size_t)avail;
      uint8_t tmp[256];
      int rd = tlsClient.read(tmp, want);
      if (rd <= 0) return false;
      for (int i = 0; i < rd; i++)
        if (*stored + 1 < cap) buf[(*stored)++] = (char)tmp[i];
      got += (size_t)rd;
    } else {
      if (!tlsClient.connected()) return false;
      delay(1);                                // poll yield, not a wait
    }
  }
  buf[*stored] = '\0';
  return true;
}

// Read into a small local scratch buffer (excess drained).
static bool tlsReadRaw(size_t n, char *scratch, size_t cap, uint32_t deadline) {
  size_t stored = 0;
  bool ok = tlsReadInto(n, scratch, cap, &stored, deadline);
  scratch[stored] = '\0';
  return ok;
}

// Read until `needle` appears at the tail of dst (needle included).
static bool tlsReadUntil(const char *needle, char *dst, size_t cap,
                         uint32_t deadline) {
  const size_t nlen = strlen(needle);
  size_t len = 0;
  dst[0] = '\0';
  while (!due(deadline)) {
    int avail = tlsClient.available();
    if (avail > 0) {
      uint8_t tmp[128];
      int want = avail > (int)sizeof tmp ? (int)sizeof tmp : avail;
      int rd = tlsClient.read(tmp, want);
      if (rd <= 0) return false;
      for (int i = 0; i < rd; i++) {
        if (len + 1 >= cap) return false;       // header too large: give up
        dst[len++] = (char)tmp[i];
      }
      dst[len] = '\0';
      if (len >= nlen && memcmp(dst + len - nlen, needle, nlen) == 0)
        return true;
    } else {
      if (!tlsClient.connected()) return false;
      delay(1);
    }
  }
  return false;
}

// Read a chunked body (defensive: Go usually sets Content-Length for our
// small responses). Chunks are appended to dst up to cap-1.
static bool tlsReadChunked(char *dst, size_t cap, size_t *outLen,
                           uint32_t deadline) {
  size_t total = 0;
  dst[0] = '\0';
  for (;;) {
    char sizeLine[32];
    if (!tlsReadUntil("\r\n", sizeLine, sizeof sizeLine, deadline)) return false;
    char *end = nullptr;
    unsigned long sz = strtoul(sizeLine, &end, 16);
    if (end == sizeLine) return false;
    if (sz == 0) {
      char crlf[4];
      if (!tlsReadRaw(2, crlf, sizeof crlf, deadline)) return false; // final CRLF
      if (crlf[0] != '\r') return false;        // trailer headers: not ours
      *outLen = total;
      return true;
    }
    if (!tlsReadInto(sz, dst, cap, &total, deadline)) return false;
    char skip[4];
    if (!tlsReadRaw(2, skip, sizeof skip, deadline)) return false;  // chunk CRLF
  }
}

// Read one HTTP response: headers, then body (Content-Length / chunked /
// read-to-close). Returns the HTTP status (0 = transport failure). When the
// message is fully consumed the connection may stay up (keep-alive reuse).
static int tlsReadResponse(uint32_t deadline, bool *reusable) {
  *reusable = false;
  if (!tlsReadUntil("\r\n\r\n", respHdr, sizeof respHdr, deadline)) return 0;
  const char *sp = strchr(respHdr, ' ');
  if (!sp) return 0;
  int status = atoi(sp + 1);
  if (status <= 0) return 0;

  size_t stored = 0;
  const char *cl = strstr(respHdr, "Content-Length:");
  const char *te = strstr(respHdr, "Transfer-Encoding:");
  bool ok;
  if (cl) {
    size_t clen = (size_t)strtoul(cl + 15, nullptr, 10);
    ok = tlsReadInto(clen, respBody, sizeof respBody, &stored, deadline);
    if (ok) *reusable = true;
  } else if (te && strstr(te, "chunked")) {
    ok = tlsReadChunked(respBody, sizeof respBody, &stored, deadline);
    if (ok) *reusable = true;
  } else {
    // No length framing: read until the server closes. Go always sets
    // Content-Length for our small JSON bodies, so this is a rare fallback
    // and the connection is never reused afterwards.
    ok = true;
    while (tlsClient.connected() || tlsClient.available() > 0) {
      if (due(deadline)) { ok = false; break; }
      int avail = tlsClient.available();
      if (avail > 0) {
        if (!tlsReadInto((size_t)avail, respBody, sizeof respBody, &stored,
                         deadline)) { ok = false; break; }
      } else {
        delay(1);
      }
    }
    *reusable = false;                          // close-delimited = not reusable
  }
  if (!ok) return 0;
  respBody[stored] = '\0';
  if (strstr(respHdr, "Connection: close")) *reusable = false;
  return status;
}

// ============================ INGEST POST ==================================
// meta reason: what the server should know about this beat (proto-v1.md).
static const char *metaReason() {
  if (camDegraded) return "camera-degraded";
  if (revoked401)  return "401-hold";
  return "";
}

// Build the meta part JSON into metaBuf (fixed buffer, snprintf only).
static void buildMeta() {
  snprintf(metaBuf, sizeof metaBuf,
           "{\"capture_id\":\"%s\",\"fw\":\"%s\",\"rssi\":%d,"
           "\"free_heap\":%u,\"cfg_ver_applied\":%lu,"
           "\"counters\":{\"boot\":%lu,\"brownout\":%lu,\"join_fail\":%lu,"
           "\"post_fail\":%lu,\"tls_fail\":%lu,\"capture_fail\":%lu},"
           "\"reason\":\"%s\"}",
           captureId, FW_VERSION,
           WiFi.status() == WL_CONNECTED ? WiFi.RSSI() : -127,
           (unsigned)ESP.getFreeHeap(), (unsigned long)cfgVerApplied,
           (unsigned long)bootCount, (unsigned long)brownoutCount,
           (unsigned long)joinFail, (unsigned long)postFail,
           (unsigned long)tlsFail, (unsigned long)captureFail,
           metaReason());
}

// One multipart POST: request head + meta part [+ frame part in 6 KiB
// chunks] + closing boundary, then read and parse the small JSON response.
// Returns the HTTP status; 0 = transport failure (counted here: tls_fail on
// connect failure, post_fail on any failed upload).  The caller retries a
// transport failure up to POST_ATTEMPTS times within the same tick.
static int ingestPost(bool haveFrame, const uint8_t *jpeg, size_t jpegLen) {
  buildMeta();
  const size_t metaLen = strlen(metaBuf);
  size_t contentLen = sizeof(PART_META_HDR) - 1 + metaLen + 2 +
                      sizeof(PART_TAIL) - 1;
  if (haveFrame)
    contentLen += sizeof(PART_FRAME_HDR) - 1 + jpegLen + 2;

  int n = snprintf(reqHead, sizeof reqHead,
                   "POST /v1/ingest HTTP/1.1\r\n"
                   "Host: %s\r\n"
                   "Authorization: Bearer %s\r\n"
                   "X-Proto: v1\r\n"
                   "Content-Type: multipart/form-data; boundary=%s\r\n"
                   "Content-Length: %lu\r\n"
                   "Connection: keep-alive\r\n"
                   "\r\n",
                   INGEST_HOST, deviceToken, MP_BOUNDARY,
                   (unsigned long)contentLen);
  if (n < 0 || (size_t)n >= sizeof reqHead) {
    Serial.println("[POST] request head overflow");
    postFail++;
    return 0;
  }

  // --- connect (or keep-alive reuse) + TLS spike measurement ---------------
  bool reused = false;
  uint32_t connectMs = 0;
  if (!tlsEnsureConnected(&reused, &connectMs)) {
    tlsFail++;
    postFail++;
    Serial.printf("[TLS ] connect failed after %lu ms (heap %u)\n",
                  (unsigned long)connectMs, (unsigned)ESP.getFreeHeap());
    return 0;
  }
  uint32_t t0 = millis();
  uint32_t deadline = t0 + POST_TIMEOUT_MS;      // 25 s for write + response

  // --- write the multipart request in chunks ------------------------------
  uint32_t tw = millis();
  bool ok = tlsWriteAll((const uint8_t *)reqHead, strlen(reqHead), deadline) &&
            tlsWriteAll((const uint8_t *)PART_META_HDR,
                        sizeof(PART_META_HDR) - 1, deadline) &&
            tlsWriteAll((const uint8_t *)metaBuf, metaLen, deadline) &&
            tlsWriteAll((const uint8_t *)"\r\n", 2, deadline);
  if (ok && haveFrame) {
    ok = tlsWriteAll((const uint8_t *)PART_FRAME_HDR,
                     sizeof(PART_FRAME_HDR) - 1, deadline);
    for (size_t off = 0; ok && off < jpegLen; off += UPLOAD_CHUNK) {
      size_t chunk = (jpegLen - off) < UPLOAD_CHUNK ? (jpegLen - off)
                                                    : UPLOAD_CHUNK;
      ok = tlsWriteAll(jpeg + off, chunk, deadline);
    }
    ok = ok && tlsWriteAll((const uint8_t *)"\r\n", 2, deadline);
  }
  ok = ok && tlsWriteAll((const uint8_t *)PART_TAIL, sizeof(PART_TAIL) - 1,
                         deadline);
  uint32_t writeMs = millis() - tw;
  if (!ok) {
    postFail++;
    tlsClient.stop();
    Serial.printf("[TLS ] handshake/connect %lu ms (reuse=%d) | write FAILED "
                  "after %lu ms (heap %u)\n",
                  (unsigned long)(reused ? 0 : connectMs), (int)reused,
                  (unsigned long)writeMs, (unsigned)ESP.getFreeHeap());
    return 0;
  }

  // --- read the response ---------------------------------------------------
  uint32_t tr = millis();
  bool reusable = false;
  int status = tlsReadResponse(deadline, &reusable);
  uint32_t respMs = millis() - tr;
  if (status == 0) {
    postFail++;
    tlsClient.stop();
    Serial.printf("[TLS ] handshake/connect %lu ms (reuse=%d) | write %lu ms | "
                  "response FAILED after %lu ms (heap %u)\n",
                  (unsigned long)(reused ? 0 : connectMs), (int)reused,
                  (unsigned long)writeMs, (unsigned long)respMs,
                  (unsigned)ESP.getFreeHeap());
    return 0;
  }
  if (reusable) tlsLastUseMs = millis();
  else tlsClient.stop();

  // TLS spike print (PLAN §5): handshake/connect ms + free heap every tick.
  Serial.printf("[TLS ] handshake/connect %lu ms (reuse=%d) | write %lu ms | "
                "resp %lu ms | total %lu ms | free heap %u\n",
                (unsigned long)(reused ? 0 : connectMs), (int)reused,
                (unsigned long)writeMs, (unsigned long)respMs,
                (unsigned long)(millis() - t0), (unsigned)ESP.getFreeHeap());
  return status;
}

// ============================ RESPONSE HANDLING ============================
// Apply desired_cfg from the response body (apply-if-found: 201/409/422/
// 429 carry it; 401 may not). Ver is echoed as cfg_ver_applied next POST.
static void applyCfgFromBody() {
  uint32_t v;
  bool any = false;
  if (jsonU32(respBody, "\"interval_s\":", &v)) {
    v = (uint32_t)constrain(v, INTERVAL_MIN_S, INTERVAL_MAX_S);
    if (v != intervalS) {
      intervalS = v;
      camPrefs.putULong("interval", v);
    }
    any = true;
  }
  if (jsonU32(respBody, "\"quality\":", &v)) {
    v = (uint32_t)constrain(v, (uint32_t)5, (uint32_t)63);
    if (v != jpegQuality) {
      jpegQuality = (uint8_t)v;
      camPrefs.putUChar("quality", (uint8_t)v);
      if (camInited) {
        sensor_t *s = esp_camera_sensor_get();
        if (s) s->set_quality(s, jpegQuality);
      }
    }
    any = true;
  }
  if (jsonU32(respBody, "\"ver\":", &v)) {
    if (v != cfgVerApplied) {
      cfgVerApplied = v;
      provPrefs.putULong("cfgver", v);
    }
    any = true;
  }
  if (any)
    Serial.printf("[CFG ] applied ver=%lu interval_s=%lu quality=%u\n",
                  (unsigned long)cfgVerApplied, (unsigned long)intervalS,
                  jpegQuality);
}

// next_s from the response body, else the configured interval.
static uint32_t respNextS() {
  uint32_t v;
  if (jsonU32(respBody, "\"next_s\":", &v) && v > 0)
    return (uint32_t)constrain(v, (uint32_t)5, (uint32_t)3600);
  return intervalS;
}

// Retry-After from response headers (429), default 10 s per proto.
static uint32_t retryAfterS() {
  const char *ra = strstr(respHdr, "Retry-After:");
  long v = ra ? strtol(ra + 12, nullptr, 10) : 0;
  if (v <= 0) v = 10;
  if (v > 3600) v = 3600;
  return (uint32_t)v;
}

// ============================ TICK =========================================
// One full tick: capture (unless revoked/degraded) -> POST -> handle
// response -> schedule the next tick. No queue: whatever happens, the next
// tick captures a fresh frame.
static void runTick() {
  nextCaptureId();
  const char *reason = metaReason();

  bool haveFrame = false;
  uint8_t *frameCopy = nullptr;
  size_t frameLen = 0;

  if (!revoked401) {                       // 401 => health-only beats
    bool attempt = false;
    if (!camDegraded && camInited) {
      attempt = true;                      // normal path
    } else if (camDegraded && due(camRetryAt)) {
      // Camera retry due: full deinit + probe (covers a dead probe and a
      // hung sensor alike). esp_camera_deinit() is safe when not inited.
      esp_camera_deinit();
      camInited = initCamera();
      if (camInited) attempt = true;
      else { captureFail++; camFailStreak++; camEnterDegraded(); }
    }

    if (attempt) {
      uint32_t tc = micros();
      camera_fb_t *fb = nullptr;
      for (int i = 0; i < 2 && !fb; i++) fb = esp_camera_fb_get();
      if (fb) {
        // Copy into PSRAM, return the frame buffer immediately (spec 5),
        // then the upload streams from our copy in 6 KiB chunks.
        frameCopy = (uint8_t *)heap_caps_malloc(fb->len, MALLOC_CAP_SPIRAM);
        if (frameCopy) {
          memcpy(frameCopy, fb->buf, fb->len);
          frameLen = fb->len;
          haveFrame = true;
        }
        esp_camera_fb_return(fb);
      }
      if (haveFrame) {
        camFailStreak = 0;
        camEverOk = true;
        if (camDegraded) {
          camDegraded = false;
          camBackoffS = 0;
          Serial.println("[CAM] recovered -> normal cadence");
        }
        Serial.printf("[CAP ] ok: %u bytes in %lu ms\n", (unsigned)frameLen,
                      (unsigned long)((micros() - tc) / 1000UL));
      } else {
        captureFail++;
        camFailStreak++;
        Serial.printf("[CAP ] failed (capture_fail=%lu, streak %u)\n",
                      (unsigned long)captureFail, camFailStreak);
        if (camFailStreak >= CAM_FAIL_LIMIT) camEnterDegraded();
      }
    }
    if (camDegraded) reason = metaReason();   // degraded may have just changed
  }

  Serial.printf("[TICK] #%lu id=%s %s reason=%s heap=%u\n",
                (unsigned long)captureSeq, captureId,
                haveFrame ? "frame" : "health-only",
                reason[0] ? reason : "-", (unsigned)ESP.getFreeHeap());

  // Transport failures get fresh chances inside the same tick: the link can
  // drop packets for seconds at a time (marginal 2.4 GHz), and one shot per
  // 60 s tick would waste the frame.  Still drop it when every attempt fails
  // (PLAN: no frame queue).
  int status = 0;
  for (uint8_t attempt = 1; attempt <= POST_ATTEMPTS && status == 0; attempt++) {
    if (attempt > 1) {
      Serial.printf("[HTTP] retry attempt %u/%u\n", attempt, POST_ATTEMPTS);
      delay(POST_RETRY_GAP_MS);
    }
    status = ingestPost(haveFrame, frameCopy, frameLen);
  }
  if (frameCopy) free(frameCopy);

  // --- response contract (docs/proto-v1.md) -------------------------------
  uint32_t sleepS = intervalS;
  if (status == 0) {               // every attempt failed: drop, retry tick
    lastHttp = 0;
    sleepS = intervalS;
    Serial.printf("[HTTP] transport failure after %u attempts -> frame dropped "
                  "(post_fail=%lu)\n", POST_ATTEMPTS, (unsigned long)postFail);
  } else {
    lastHttp = status;
    applyCfgFromBody();
    if (status == 201 || status == 409) {   // 409 duplicate = same body
      if (revoked401) {
        revoked401 = false;
        revokedBackoffS = REVOKED_BACKOFF_MIN;
        Serial.println("[HTTP] token accepted again -> normal cadence");
      }
      ledOkFlash();
      sleepS = respNextS();
      Serial.printf("[HTTP] %d ok -> sleep %lu s\n", status,
                    (unsigned long)sleepS);
    } else if (status == 401) {             // revoked: keep identity, back off
      if (!revoked401) {
        revoked401 = true;
        revokedBackoffS = REVOKED_BACKOFF_MIN;
      } else if (revokedBackoffS < REVOKED_BACKOFF_MAX) {
        revokedBackoffS *= 2;
        if (revokedBackoffS > REVOKED_BACKOFF_MAX)
          revokedBackoffS = REVOKED_BACKOFF_MAX;
      }
      sleepS = revokedBackoffS;
      Serial.printf("[HTTP] 401 revoked -> health-only beats every %lu s, "
                    "identity kept\n", (unsigned long)sleepS);
    } else if (status == 429) {             // rate-limited: sleep retry_after
      sleepS = retryAfterS();
      Serial.printf("[HTTP] 429 rate-limited -> sleep %lu s\n",
                    (unsigned long)sleepS);
    } else if (status == 422 || status == 413) {  // rejected: log, not fatal
      char why[48] = {0};
      jsonStr(respBody, "\"reason\":", why, sizeof why);
      sleepS = respNextS();
      Serial.printf("[HTTP] %d rejected (%s) -> not fatal, sleep %lu s\n",
                    status, why[0] ? why : "?", (unsigned long)sleepS);
    } else {                                // 507 / 5xx: retry next tick
      postFail++;
      sleepS = intervalS;
      Serial.printf("[HTTP] %d server-side failure -> retry next tick "
                    "(post_fail=%lu)\n", status, (unsigned long)postFail);
    }
  }

  nextTickAt = millis() + sleepS * 1000UL;
  Serial.printf("[TICK] next in %lu s (interval_s=%lu, cfg_ver=%lu, heap=%u)\n",
                (unsigned long)sleepS, (unsigned long)intervalS,
                (unsigned long)cfgVerApplied, (unsigned)ESP.getFreeHeap());
}

// Gate: run a tick only when one is due AND the link is up. A due tick
// waits (without blocking) for WiFi — capture never happens into the void.
static void tickService() {
  if (!due(nextTickAt)) return;
  if (WiFi.status() != WL_CONNECTED) return;
  runTick();
}

// ============================ LAN CAGE (port 80) ===========================
// Installer-facing only: /api/status, /jpg (rate-limited), /ping and the
// caged factory reset. No stream, no settings form, no files, no OTA page.
static void handleStatus() {
  char j[1024];
  const char *camState = camDegraded ? (camEverOk ? "degraded" : "init-failed")
                                     : "ok";
  uint32_t nextIn = due(nextTickAt) ? 0 : (nextTickAt - millis()) / 1000;
  snprintf(j, sizeof j,
           "{\"hostname\":\"%s\",\"ip\":\"%s\",\"rssi\":%d,\"wifi\":%s,"
           "\"free_heap\":%u,\"free_psram\":%u,\"uptime_s\":%lu,"
           "\"fw\":\"%s\",\"serial\":\"%s\","
           "\"boot\":%lu,\"brownout\":%lu,\"join_fail\":%lu,\"post_fail\":%lu,"
           "\"tls_fail\":%lu,\"capture_fail\":%lu,\"capture_seq\":%lu,"
           "\"cfg_ver_applied\":%lu,\"interval_s\":%lu,\"quality\":%u,"
           "\"next_tick_in_s\":%lu,\"camera\":\"%s\",\"mode\":\"%s\","
           "\"last_http\":%d}",
           mdnsName, WiFi.localIP().toString().c_str(), WiFi.RSSI(),
           WiFi.status() == WL_CONNECTED ? "true" : "false",
           (unsigned)ESP.getFreeHeap(),
           (unsigned)(psramFound() ? ESP.getFreePsram() : 0),
           (unsigned long)(millis() / 1000UL), FW_VERSION, serialNo,
           (unsigned long)bootCount, (unsigned long)brownoutCount,
           (unsigned long)joinFail, (unsigned long)postFail,
           (unsigned long)tlsFail, (unsigned long)captureFail,
           (unsigned long)captureSeq, (unsigned long)cfgVerApplied,
           (unsigned long)intervalS, jpegQuality, (unsigned long)nextIn,
           camState, revoked401 ? "revoked-401" : "normal", lastHttp);
  server.send(200, "application/json", j);
}

// One still per 3 s max; 503 while the camera is not inited. Counters are
// ingest-path only — an installer pull does not move them.
static void handleJpg() {
  static uint32_t lastJpgAt = 0;
  uint32_t now = millis();
  if (now - lastJpgAt < JPG_RATE_MS) {
    server.send(429, "text/plain", "rate limited: 1 per 3 s");
    return;
  }
  lastJpgAt = now;
  if (!camInited) {
    server.send(503, "text/plain", "camera not inited (degraded)");
    return;
  }
  camera_fb_t *fb = esp_camera_fb_get();
  if (!fb) {
    server.send(503, "text/plain", "capture failed");
    return;
  }
  WiFiClient wc = server.client();
  wc.setTimeout(1000);
  server.setContentLength(fb->len);
  server.send(200, "image/jpeg", "");
  wc.write(fb->buf, fb->len);
  esp_camera_fb_return(fb);
}

static void handlePing() { server.send(200, "text/plain", "ok"); }

// Caged factory reset — requires the explicit ?confirm=1; the response goes
// out first, the reset follows shortly after so the client sees it.
static void handleFactoryReset() {
  if (server.arg("confirm") != "1") {
    server.send(400, "text/plain", "confirm=1 required");
    return;
  }
  server.send(200, "text/plain",
              "clearing 'prov' + 'cam' (identity, counters, config), restarting");
  pendingFactoryAt = millis() + 400;
}

static void handleNotFound() { server.send(404, "text/plain", "not found"); }

static void startWebServer() {
  server.on("/api/status", HTTP_GET, handleStatus);
  server.on("/jpg", HTTP_GET, handleJpg);
  server.on("/ping", HTTP_GET, handlePing);
  server.on("/api/factory-reset", HTTP_POST, handleFactoryReset);
  server.onNotFound(handleNotFound);
  server.begin();
}

// mDNS: imaliguzve-<serial>.local (serial = efuse MAC), advertises http/80.
static void startMdns() {
  if (MDNS.begin(mdnsName)) {
    MDNS.addService("http", "tcp", 80);
    Serial.printf("[MDNS] http://%s.local\n", mdnsName);
  } else {
    Serial.println("[MDNS] begin failed");
  }
}

// ============================ SERIAL CONSOLE ===============================
// USB provisioning path: "reset" clears 'prov' + 'cam' and restarts.
static void handleSerialCmd(char *cmd) {
  if (strcmp(cmd, "reset") == 0) {
    factoryReset();
  } else {
    Serial.printf("[CLI] unknown command '%s' (try: reset)\n", cmd);
  }
}

static void serialPoll() {
  static char line[32];
  static size_t n = 0;
  while (Serial.available()) {
    char c = (char)Serial.read();
    if (c == '\n' || c == '\r') {
      if (n) {
        line[n] = 0;
        handleSerialCmd(line);
        n = 0;
      }
    } else if (n < sizeof line - 1) {
      line[n++] = c;
    }
  }
}

// ============================ SETUP / LOOP =================================
// Order: serial -> LED -> identity (efuse MAC) -> NVS -> camera (never
// fatal) -> TLS budget -> WiFi (begin once) -> LAN cage -> mDNS -> schedule.
// WiFi MUST precede the LAN cage: WiFi.mode()/begin() is what starts the
// lwIP tcpip thread, and WebServer.begin() creates a socket — doing that
// first trips `assert(sys_mbox_valid)` ("Invalid mbox") and reboot-loops.
// No NTP anywhere: capture never waits for time.
void setup() {
  Serial.begin(115200);
  delay(100);
  Serial.println();
  Serial.println("=== imaliguzve firmware " FW_VERSION " ===");

  pinMode(INDICATOR_LED_PIN, OUTPUT);
  ledWrite(false);

  uint64_t mac = ESP.getEfuseMac();
  snprintf(serialNo, sizeof serialNo, "%06x", (uint32_t)(mac & 0xFFFFFF));
  snprintf(mdnsName, sizeof mdnsName, "%s-%s", MDNS_HOST_PREFIX, serialNo);

  loadNvs();

  camInited = initCamera();
  if (camInited) {
    camEverOk = true;
  } else {
    // Camera failure is NOT fatal (PLAN §5): degraded mode, no reboot loop.
    camEnterDegraded();
  }

  tlsClient.setHandshakeTimeout(TLS_HANDSHAKE_S);   // connect budget (see TLS_HANDSHAKE_S)
#if INSECURE_TLS
  tlsClient.setInsecure();  // DEV ONLY — prod grep-gates INSECURE_TLS=0
#endif

  startWiFi();      // first: brings up the lwIP tcpip thread
  startWebServer(); // LAN cage — needs sockets, so after WiFi
  startMdns();

  nextTickAt = millis() + 5000;                     // first beat after 5 s

  Serial.printf("[BOOT] serial=%s boot=%lu brownout=%lu cfg_ver=%lu "
                "interval_s=%lu quality=%u\n",
                serialNo, (unsigned long)bootCount, (unsigned long)brownoutCount,
                (unsigned long)cfgVerApplied, (unsigned long)intervalS,
                jpegQuality);
  // Never print the token itself — only its presence/length (secret).
  Serial.printf("[BOOT] PSRAM=%s free_heap=%u camera=%s token_len=%u\n",
                psramFound() ? "yes" : "no", (unsigned)ESP.getFreeHeap(),
                camInited ? "ok" : "DEGRADED",
                (unsigned)strlen(deviceToken));
}

void loop() {
  serviceWiFi();                 // non-blocking reconnect with backoff
  server.handleClient();         // LAN cage
  tickService();                 // capture + HTTPS ingest when due
  serialPoll();                  // "reset" over USB
  ledService();                  // millis()-based LED patterns
  if (pendingFactoryAt != 0 && due(pendingFactoryAt)) factoryReset();
  delay(2);                      // yield — not a wait for anything
}
