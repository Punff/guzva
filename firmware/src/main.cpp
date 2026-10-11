/*
 * ============================================================================
 *  guzva — ESP32-CAM capture firmware (fresh build, PLAN §5)
 *  AI-Thinker ESP32-CAM / OV2640 · PlatformIO + arduino-esp32 + esp32-camera
 *  https://github.com/espressif/esp32-camera
 * ----------------------------------------------------------------------------
 *  Does:
 *    - capture a JPEG on a timer (cadence driven by server desired_cfg)
 *    - hand-built multipart POST to https://<INGEST_HOST>/v1/ingest
 *      (part "meta" JSON + optional part "frame", per proto-v1)
 *    - apply desired_cfg with read-back echo (cfg_ver_applied on every POST):
 *      cadence (interval_s) + the FULL OV2640 feature set — framesize,
 *      JPEG quality, brightness, contrast, saturation, sharpness, denoise,
 *      special effects, white-balance mode/AWB, exposure (AEC/ae_level),
 *      gain (AGC/agc_gain), hmirror, vflip, dcw, lens correction, colorbar
 *      test pattern and the flash LED PWM — any subset, server-driven
 *    - no frame queue: upload failure = drop frame, post_fail++, fresh
 *      capture on the next tick
 *    - TLS: WiFiClientSecure + embedded CA (setCACert); keep-alive reuse of
 *      one client across ticks; 12 s handshake / 12 s connect / 25 s POST
 *      budgets (bench-proven: covers lwIP 0/3/9 s SYN + 9 s CH retransmit)
 *    - non-blocking WiFi manager (WiFi.begin once, backoff retries from
 *      loop(); capture never waits for WiFi, there is no NTP in v1)
 *    - camera failure => exponential backoff + health-only beats, never a
 *      hot-reboot loop; XCLK 20 MHz → 16 MHz fallback
 *    - NVS identity in "prov", sensor/device config in "cam", factory reset
 *      over USB serial ("reset") and the caged LAN endpoint
 *    - LAN cage on port 80: GET /api/status, GET /jpg (1 per 3 s),
 *      GET /ping, POST /api/factory-reset?confirm=1 — nothing else
 *    - mDNS guzva-<serial>.local, serial from efuse MAC
 *    - status LED GPIO33 (active LOW), all patterns millis()-based
 *
 *  Deliberately NOT here (PLAN §5 "prod builds delete"): port-81 MJPEG
 *  stream, motion, timelapse, SD/SPIFFS photo storage, GPIO switchboard,
 *  OTA upload form, RGB/grayscale endpoints, the big web dashboard, NTP.
 *
 *  Counters: boot + brownout persist in NVS ("prov"); join_fail / post_fail /
 *  tls_fail / capture_fail live in RAM and reset each boot — intentional.
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
#include <esp_task_wdt.h>

#include "settings.h"

// TLS escape hatch — the production value (-DINSECURE_TLS=0) lives in
// platformio.ini build_flags and is grep-gated: prod builds must show
// INSECURE_TLS=0 and no reachable setInsecure() outside this guard.
#ifndef INSECURE_TLS
#define INSECURE_TLS 0
#endif

// Firmware version — reported in every meta "fw" field (proto-v1) and the
// serial banner. tools/fwversion.py stamps `git describe` into
// include/fw_version.h at build time (W4); "dev" means an unstamped build,
// which only happens outside the repo — the fleet board flags it.
#if __has_include("fw_version.h")
#include "fw_version.h"
#endif
#ifndef FW_VERSION
#define FW_VERSION "dev"
#endif

// ============================ HARDWARE PINS ================================
// AI-Thinker ESP32-CAM camera wiring (classic mapping: Y2..Y9 =
// 5,18,19,21,36,39,34,35) — fixed by the board, proven on this hardware.
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

#define FLASH_GPIO_NUM     4        // white flash LED (PWM via LEDC ch 1)
#define INDICATOR_LED_PIN  33       // status LED, active LOW
#define FLASH_LEDC_CH      1        // ch 0 / timer 0 = camera XCLK
#define FLASH_LEDC_FREQ    5000
#define FLASH_LEDC_BITS    8

// ============================ LIMITS & TUNABLES ============================
static const uint32_t WIFI_RETRY_MIN_MS   = 10000;   // join backoff 10 s .. 60 s
static const uint32_t WIFI_RETRY_MAX_MS   = 60000;
// Bench-measured (2026-10-08): lwIP RTO ≈ 3 s on the weak link → SYN at
// 0/3/9 s, ClientHello retransmit at 9 s. An 8 s budget cut the connection
// off BEFORE the retransmit; 12 s covers it (ops-log).
// TLS_CONNECT_MS is DERIVED, never written again: the same budget once
// lived here twice in two units and drifted by construction (W4/F4).
static const uint32_t TLS_HANDSHAKE_S     = 12;
static const uint32_t TLS_CONNECT_MS     = TLS_HANDSHAKE_S * 1000UL;
static const uint32_t POST_TIMEOUT_MS     = 25000;   // 25 s POST (write+response)
static const uint8_t  POST_ATTEMPTS       = 3;       // transport retries/tick
static const uint32_t POST_RETRY_GAP_MS   = 6000;    // decorrelate bursts
static const uint32_t KEEPALIVE_IDLE_MS   = 45000;   // reuse well under 60 s idle
static const uint32_t JPG_RATE_MS         = 3000;    // /jpg: 1 per 3 s
static const size_t   UPLOAD_CHUNK        = 6144;    // 6 KiB chunks (spec 4-8 KiB)
static const uint32_t INTERVAL_MIN_S      = 30;      // proto-v1 range
static const uint32_t INTERVAL_MAX_S      = 300;
static const uint32_t REVOKED_BACKOFF_MIN = 60;      // 401: 60 -> 600 s (proto)
static const uint32_t REVOKED_BACKOFF_MAX = 600;
static const uint32_t CAM_BACKOFF_MIN_S   = 10;      // camera probe backoff
static const uint32_t CAM_BACKOFF_MAX_S   = 1800;
static const uint8_t  CAM_FAIL_LIMIT      = 5;       // degraded after 5
static const uint8_t  FRAMESIZE_MAX_OV2640 = 10;     // FRAMESIZE_UXGA
// Task-watchdog backstop (W4/F1). Every blocking path in this firmware has
// its own deadline, but before W4 nothing recovered an UNEXPECTED stall: a
// driver edge or a logic deadlock hung the board forever with the LED dark.
// The WDT is armed above the worst legitimate blocking envelope — one tick
// = capture ≈8 s + 3×(12 s connect + 25 s POST) + 2×6 s gaps ≈ 131 s — so
// it only ever fires on a true stall, and a fire reboots into an honest
// STALE gap instead of a silent hang. (This replaces the spec's old
// "hourly-max reboot": the watchdog is the mechanism that was meant.)
static const uint32_t WDT_TIMEOUT_S       = 150;

// ============================ MULTIPART (proto-v1) =========================
#define MP_BOUNDARY "guzva-v1-9f3a"
static const char PART_META_HDR[] =
    "--" MP_BOUNDARY "\r\n"
    "Content-Disposition: form-data; name=\"meta\"\r\n"
    "Content-Type: application/json\r\n\r\n";
static const char PART_FRAME_HDR[] =
    "--" MP_BOUNDARY "\r\n"
    "Content-Disposition: form-data; name=\"frame\"; filename=\"frame.jpg\"\r\n"
    "Content-Type: image/jpeg\r\n\r\n";
static const char PART_TAIL[] = "--" MP_BOUNDARY "--\r\n";

// ============================ SENSOR CONFIG (OV2640) =======================
// The full device feature set (esp32-camera sensor_t). Any subset arrives in
// desired_cfg, gets applied to the sensor, persisted in NVS and echoed via
// cfg_ver_applied. Values are clamped to what the OV2640 accepts.
struct SensorCfg {
  uint8_t framesize;    // FRAMESIZE_* (0..UXGA)  — OV2640 max 10
  uint8_t quality;      // JPEG 4..63, lower = better
  int8_t  brightness;   // -2..2
  int8_t  contrast;     // -2..2
  int8_t  saturation;   // -2..2
  int8_t  sharpness;    // -2..2 (NULL-guarded: not on all sensors)
  uint8_t denoise;      // 0/1
  uint8_t special_fx;   // 0 normal .. 6 sepia
  uint8_t wb_mode;      // 0 auto .. 4 home
  uint8_t awb;          // 0/1 white balance
  uint8_t aec;          // 0/1 exposure control
  uint8_t agc;          // 0/1 gain control
  int8_t  ae_level;     // -2..2
  uint8_t agc_gain;     // 0..30
  uint8_t hmirror;      // 0/1
  uint8_t vflip;        // 0/1
  uint8_t dcw;          // 0/1 downsize
  uint8_t lenc;         // 0/1 lens correction
  uint8_t colorbar;     // 0/1 test pattern
  uint8_t led_pwm;      // 0..255 flash LED
};
static SensorCfg scDefault = {
  /* framesize  */ 5,       // FRAMESIZE_QVGA (320x240): the render grid is
                             // 160x120, so anything above QVGA only costs
                             // upload bytes on a weak link (DESIGN.md ⚑10).
                             // NOTE: 4 is 240x240 (square, non-4:3) — the old
                             // default here was mislabelled VGA and its frames
                             // are dims-rejected server-side.
  /* quality    */ 14,
  /* brightness */ 0, /* contrast */ 0, /* saturation */ 0, /* sharpness */ 0,
  /* denoise    */ 0,
  /* special_fx */ 0, /* wb_mode */ 0,
  /* awb        */ 1, /* aec */ 1, /* agc */ 1,
  /* ae_level   */ 0, /* agc_gain */ 0,
  /* hmirror    */ 0, /* vflip */ 0, /* dcw */ 1,
  /* lenc       */ 1, /* colorbar */ 0,
  /* led_pwm    */ 0,
};
static SensorCfg sc = scDefault;

// ============================ STATE ========================================
// --- NVS-backed -------------------------------------------------------------
//   namespace "prov": boot, brownout, capture (id counter), cfgver, token
//   namespace "cam" : interval (s), scfg (SensorCfg blob)
Preferences provPrefs, camPrefs;

uint32_t bootCount      = 0;
uint32_t brownoutCount  = 0;
uint32_t captureSeq     = 0;
uint32_t cfgVerApplied  = 0;
char     deviceToken[80] = {0};
uint32_t intervalS      = 60; // server default; 45-90 s recommended (DESIGN.md ⚑10)

// --- RAM counters (reset on boot) -------------------------------------------
uint32_t joinFail = 0, postFail = 0, tlsFail = 0, captureFail = 0;

// --- runtime ----------------------------------------------------------------
char     serialNo[8]     = {0};
char     mdnsName[40]    = {0};
char     captureId[40]   = {0};
char     metaBuf[1024]   = {0};
char     reqHead[640]    = {0};
char     respHdr[1024]   = {0};
char     respBody[2048]  = {0};   // room for a full sensor desired_cfg
// Bytes read past the end of the header block (the body often arrives in the
// same TLS record); the body read consumes these before touching the socket.
char     respCarry[512]  = {0};
size_t   respCarryLen    = 0;

WiFiClientSecure tlsClient;
WebServer server(80);

uint32_t tlsLastUseMs    = 0;
bool     camInited       = false;
bool     camEverOk       = false;
bool     camDegraded     = false;
uint8_t  camFailStreak   = 0;
uint32_t camBackoffS     = 0;
uint32_t camRetryAt      = 0;

bool     revoked401      = false;
uint32_t revokedBackoffS = REVOKED_BACKOFF_MIN;

uint32_t nextTickAt      = 0;
int      lastHttp        = 0;
uint32_t ledOkFlashUntil = 0;
uint32_t pendingFactoryAt = 0;
uint32_t wifiLastAttempt = 0;
uint32_t wifiRetryMs     = WIFI_RETRY_MIN_MS;
bool     wifiWasDown     = true;

// ============================ SMALL HELPERS ================================
static bool due(uint32_t at) { return (int32_t)(millis() - at) >= 0; }

static int32_t clampi(int32_t v, int32_t lo, int32_t hi) {
  return v < lo ? lo : (v > hi ? hi : v);
}
static uint32_t clampu(uint32_t v, uint32_t lo, uint32_t hi) {
  return v < lo ? lo : (v > hi ? hi : v);
}

// Tiny JSON reader: find `"key":` and parse the unsigned decimal after it.
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

// Signed variant for the -2..2 sensor fields.
static bool jsonS32(const char *body, const char *key, int32_t *out) {
  const char *p = strstr(body, key);
  if (!p) return false;
  p += strlen(key);
  while (*p == ' ' || *p == '\t' || *p == '\r' || *p == '\n') p++;
  bool neg = (*p == '-');
  if (neg) p++;
  if (*p < '0' || *p > '9') return false;
  char *end = nullptr;
  long v = strtol(p, &end, 10);
  if (end == p) return false;
  *out = neg ? -(int32_t)v : (int32_t)v;
  return true;
}

// Tiny JSON string reader: `"key":"` → copy until the closing quote.
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
//   connecting = 2 Hz blink · ok = short flash after a good POST
//   revoked401 = double-blink per 1.2 s · camera-degraded = slow 0.5 Hz pulse
static void ledWrite(bool on) { digitalWrite(INDICATOR_LED_PIN, on ? LOW : HIGH); }
static void ledOkFlash()      { ledOkFlashUntil = millis() + 150; }

static void ledService() {
  uint32_t now = millis();
  if (ledOkFlashUntil != 0 && !due(ledOkFlashUntil)) { ledWrite(true); return; }
  if (revoked401) {
    uint32_t ph = now % 1200;
    ledWrite(ph < 90 || (ph >= 200 && ph < 290));
  } else if (camDegraded) {
    ledWrite((now % 2000) < 1000);
  } else if (WiFi.status() != WL_CONNECTED) {
    ledWrite((now % 500) < 250);
  } else {
    ledWrite(false);
  }
}

// ============================ SENSOR APPLY =================================
// Apply the whole SensorCfg to the running sensor (called after init and
// whenever desired_cfg changes something). Function-pointer guarded: the
// OV2640 driver does not implement every sensor_t hook.
static void applySensorCfg() {
  ledcWrite(FLASH_LEDC_CH, sc.led_pwm);
  sensor_t *s = esp_camera_sensor_get();
  if (!s) return;
  s->set_framesize(s, (framesize_t)sc.framesize);
  s->set_quality(s, sc.quality);
  if (s->set_brightness)  s->set_brightness(s, sc.brightness);
  if (s->set_contrast)    s->set_contrast(s, sc.contrast);
  if (s->set_saturation)  s->set_saturation(s, sc.saturation);
  if (s->set_sharpness)   s->set_sharpness(s, sc.sharpness);
  if (s->set_denoise)     s->set_denoise(s, sc.denoise);
  if (s->set_special_effect) s->set_special_effect(s, sc.special_fx);
  if (s->set_wb_mode)     s->set_wb_mode(s, sc.wb_mode);
  if (s->set_whitebal)    s->set_whitebal(s, sc.awb);
  if (s->set_exposure_ctrl) s->set_exposure_ctrl(s, sc.aec);
  if (s->set_gain_ctrl)   s->set_gain_ctrl(s, sc.agc);
  if (s->set_ae_level)    s->set_ae_level(s, sc.ae_level);
  if (s->set_agc_gain)    s->set_agc_gain(s, sc.agc_gain);
  if (s->set_hmirror)     s->set_hmirror(s, sc.hmirror);
  if (s->set_vflip)       s->set_vflip(s, sc.vflip);
  if (s->set_dcw)         s->set_dcw(s, sc.dcw);
  if (s->set_lenc)        s->set_lenc(s, sc.lenc);
  if (s->set_colorbar)    s->set_colorbar(s, sc.colorbar);
}

static void saveSensorCfg() { camPrefs.putBytes("scfg", &sc, sizeof sc); }

// ============================ NVS / IDENTITY ===============================
static void loadNvs() {
  provPrefs.begin("prov", false);
  camPrefs.begin("cam", false);

  bootCount = provPrefs.getULong("boot", 0) + 1;
  provPrefs.putULong("boot", bootCount);

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
  intervalS = (uint32_t)clampu(iv, INTERVAL_MIN_S, INTERVAL_MAX_S);

  sc = scDefault;
  size_t n = camPrefs.getBytes("scfg", &sc, sizeof sc);
  if (n != sizeof sc) sc = scDefault;   // first boot or schema change
}

static void factoryReset() {
  Serial.println("[NVS] factory reset: clearing 'prov' + 'cam'");
  provPrefs.clear();
  camPrefs.clear();
  Serial.println("[NVS] cleared, restarting");
  delay(100);
  ESP.restart();
}

// capture_id = "C<boot>-<ctr>-<rand4>" — matches proto
// ^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$ ; monotonic counter in NVS,
// issued every tick (frames and health-only beats alike): idempotency key.
static void nextCaptureId() {
  captureSeq++;
  provPrefs.putULong("capture", captureSeq);
  uint32_t r = esp_random() & 0xFFFF;
  snprintf(captureId, sizeof captureId, "C%lu-%lu-%04lx",
           (unsigned long)bootCount, (unsigned long)captureSeq,
           (unsigned long)r);
}

// ============================ CAMERA =======================================
static bool cameraTryInit(uint32_t xclkHz) {
  camera_config_t c = {};
  c.ledc_channel = LEDC_CHANNEL_0;    // XCLK owns LEDC ch0/timer0 (flash: ch1)
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
  c.frame_size   = psramFound() ? (framesize_t)sc.framesize : FRAMESIZE_QVGA;
  c.jpeg_quality = sc.quality;
  c.fb_location  = psramFound() ? CAMERA_FB_IN_PSRAM : CAMERA_FB_IN_DRAM;
  c.grab_mode    = CAMERA_GRAB_LATEST;
  c.fb_count     = psramFound() ? 2 : 1;

  esp_err_t err = esp_camera_init(&c);
  if (err != ESP_OK) {
    Serial.printf("[CAM] init failed 0x%x (xclk %lu MHz)\n", err,
                  (unsigned long)(xclkHz / 1000000UL));
    return false;
  }
  applySensorCfg();     // NVS sensor config (not just the boot defaults)
  return true;
}

// 20 MHz XCLK first, 16 MHz fallback (bench: thin supply kills 20 MHz).
static bool initCamera() {
  if (cameraTryInit(20000000)) return true;
  Serial.println("[CAM] 20 MHz XCLK failed -> 16 MHz fallback");
  return cameraTryInit(16000000);
}

static void camEnterDegraded() {
  camDegraded = true;
  camBackoffS = camBackoffS ? camBackoffS * 2 : CAM_BACKOFF_MIN_S;
  if (camBackoffS > CAM_BACKOFF_MAX_S) camBackoffS = CAM_BACKOFF_MAX_S;
  camRetryAt = millis() + camBackoffS * 1000UL;
  Serial.printf("[CAM] degraded: health-only beats, camera retry in %lu s\n",
                (unsigned long)camBackoffS);
}

// ============================ WIFI MANAGER =================================
// WiFi.begin exactly once; reconnects retried from loop() with backoff.
// There is never a delay() waiting for WiFi — a due ingest tick waits at the
// gate until the link is back (no queue, no stale frames).
static bool wifiScanOnce = false;
static bool wifiEverUp = false;

static void wifiEvent(arduino_event_id_t id, arduino_event_info_t info) {
  switch (id) {
    case ARDUINO_EVENT_WIFI_STA_DISCONNECTED:
      Serial.printf("[WiFi] disconnected reason=%u\n",
                    info.wifi_sta_disconnected.reason);
      break;
    case ARDUINO_EVENT_WIFI_STA_GOT_IP:
      Serial.printf("[WiFi] got IP %s rssi=%d\n",
                    WiFi.localIP().toString().c_str(), (int)WiFi.RSSI());
      break;
    default:
      break;
  }
}

// One visibility scan per boot, from the retry path (radio idle — scanning
// mid-join fails).
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
  WiFi.mode(WIFI_STA);              // starts the lwIP tcpip thread — MUST run
  WiFi.setHostname(mdnsName);       // before any socket (WebServer) is created
  WiFi.setSleep(false);
  WiFi.setTxPower(WIFI_POWER_19_5dBm);
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
  if (!wifiWasDown) {
    wifiWasDown = true;
    wifiLastAttempt = millis();
    tlsClient.stop();               // stale TLS session is useless
    if (wifiEverUp) Serial.println("[WiFi] link lost, retry with backoff");
    return;
  }
  if (millis() - wifiLastAttempt < wifiRetryMs) return;
  joinFail++;
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
// Failure diagnostics (printed by the caller): the connect duration, then the
// post-mortem TCP probe result, then mbedTLS's own error text.
static uint32_t diagTcpMs = 0;
static bool     diagTcpOk = false;
static int      diagSslErr = 0;
static char     diagSslMsg[64] = {0};

// Classify a failed connect with ONE plain TCP probe on a fresh socket.
// Probe OK -> the path was fine, the handshake itself died (ssl_err says
// why); probe FAIL -> SYNs are not getting through (route/RF). Runs only
// after a failure, so a healthy tick opens exactly one connection instead of
// two — no probe traffic on the air when things work. 6 s = SYNs at 0/3 s,
// one lwIP retransmit before judging the path.
static void tlsDiagnose() {
  uint32_t t = millis();
  WiFiClient probe;
  diagTcpOk = probe.connect(INGEST_HOST, INGEST_PORT, 6000) != 0;
  diagTcpMs = millis() - t;
  probe.stop();
}

// Reuse the single WiFiClientSecure across ticks while it is still up and
// its idle window is under the keep-alive budget; otherwise fresh handshake.
static bool tlsEnsureConnected(bool *reused, uint32_t *connectMs) {
  *reused = false;
  *connectMs = 0;
  diagTcpMs = 0;
  diagTcpOk = false;
  diagSslErr = 0;
  diagSslMsg[0] = 0;
  uint32_t now = millis();
  if (tlsClient.connected() && (now - tlsLastUseMs) < KEEPALIVE_IDLE_MS) {
    *reused = true;
    return true;
  }
  if (tlsClient.connected()) tlsClient.stop();

#if INSECURE_TLS
  tlsClient.setInsecure();  // DEV ONLY — prod grep-gates INSECURE_TLS=0
#else
  tlsClient.setCACert(CA_ROOT_PEM);
#endif
  // Re-arm before every connect: after stop() the core can leave the
  // handshake timeout at 0 and the handshake fails instantly (arduino-esp32
  // issue #6077).
  tlsClient.setHandshakeTimeout(TLS_HANDSHAKE_S);
  uint32_t t1 = millis();
  bool ok = tlsClient.connect(INGEST_HOST, INGEST_PORT,
                              (int32_t)TLS_CONNECT_MS) != 0;
  *connectMs = millis() - t1;
  if (!ok) {
    diagSslErr = tlsClient.lastError(diagSslMsg, sizeof diagSslMsg);
    tlsClient.stop();
    tlsDiagnose();
    return false;
  }
  tlsClient.setTimeout(POST_TIMEOUT_MS / 1000);   // seconds: socket r/w budget
  return true;
}

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

// Read exactly n bytes; stored into buf[*stored .. cap-2] while they fit,
// the rest drained so keep-alive stays usable.
// Read exactly n bytes; stored into buf[*stored .. cap-2] while they fit,
// the rest drained so keep-alive stays usable. Bytes already pulled out of
// the TLS stream by the header read (respCarry) are consumed first — they
// are gone from mbedtls's buffer, so reading from the socket alone would
// stall waiting for data that has already been seen.
static bool tlsReadInto(size_t n, char *buf, size_t cap, size_t *stored,
                        uint32_t deadline) {
  size_t got = 0, carry = 0;
  while (got < n) {
    if (due(deadline)) return false;
    while (got < n && carry < respCarryLen) {
      if (*stored + 1 >= cap) return false;
      buf[(*stored)++] = respCarry[carry++];
      got++;
    }
    if (got >= n) break;
    respCarryLen = carry = 0;  // carry drained
    int avail = tlsClient.available();
    size_t want = n - got;
    if (want > 256) want = 256;
    if (avail > 0 && (size_t)avail < want) want = (size_t)avail;
    uint8_t tmp[256];
    int rd = tlsClient.read(tmp, want);
    if (rd > 0) {
      for (int i = 0; i < rd; i++)
        if (*stored + 1 < cap) buf[(*stored)++] = (char)tmp[i];
      got += (size_t)rd;
    } else {
      if (rd < 0 && !tlsClient.connected()) return false;
      delay(1);                      // poll yield, not a wait
    }
  }
  buf[*stored] = '\0';
  return true;
}

static bool tlsReadRaw(size_t n, char *scratch, size_t cap, uint32_t deadline) {
  size_t stored = 0;
  bool ok = tlsReadInto(n, scratch, cap, &stored, deadline);
  scratch[stored] = '\0';
  return ok;
}

// Read until `needle` appears in dst (needle included).
//
// available() is itself the pump (WiFiClientSecure::data_to_read calls
// mbedtls_ssl_read(NULL,0) to pull+decrypt, then reports what is buffered), and
// read() is gated on it — so the 1-byte probe below is belt-and-braces for the
// window where available() reports 0, not the primary mechanism.
//
// Two things this must get right, both of which were bugs:
//   1. the needle is scanned at every position the new bytes could have
//      completed it at, not just at the tail (see below);
//   2. bytes read past the needle are carried into the body read (respCarry),
//      because they are already out of mbedtls and re-reading the socket stalls.
static bool tlsReadUntil(const char *needle, char *dst, size_t cap,
                         uint32_t deadline) {
  const size_t nlen = strlen(needle);
  size_t len = 0;
  dst[0] = '\0';
  while (!due(deadline)) {
    int avail = tlsClient.available();
    uint8_t tmp[128];
    size_t want = avail > 0 ? (size_t)avail : 1;  // 1-byte probe when idle
    if (want > sizeof tmp) want = sizeof tmp;
    size_t before = len;
    int rd = tlsClient.read(tmp, want);
    if (rd > 0) {
      for (int i = 0; i < rd; i++) {
        if (len + 1 >= cap) return false;
        dst[len++] = (char)tmp[i];
      }
      dst[len] = '\0';
      // Scan every position the new bytes could have completed the needle at.
      // Testing only the tail misses it whenever it lands mid-read (reads are
      // capped at 128 B, so a 313 B response put "\r\n\r\n" at ~150 and a
      // complete, valid 201 was discarded at the deadline).
      size_t from = (before + 1 > nlen) ? (before + 1 - nlen) : 0;
      for (size_t i = from; i + nlen <= len; i++) {
        if (memcmp(dst + i, needle, nlen) == 0) {
          size_t end = i + nlen;    // keep through the needle
          // Whatever followed it (usually the body) has already been pulled
          // from mbedtls — hand it on rather than dropping it.
          respCarryLen = len - end;
          if (respCarryLen > sizeof respCarry) respCarryLen = sizeof respCarry;
          memcpy(respCarry, dst + end, respCarryLen);
          len = end;
          dst[len] = '\0';
          return true;
        }
      }
    } else {
      if (rd < 0 && !tlsClient.connected()) return false;
      delay(1);  // nothing yet — yield, do not spin on available()
    }
  }
  return false;
}

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
      if (!tlsReadRaw(2, crlf, sizeof crlf, deadline)) return false;
      if (crlf[0] != '\r') return false;
      *outLen = total;
      return true;
    }
    if (!tlsReadInto(sz, dst, cap, &total, deadline)) return false;
    char skip[4];
    if (!tlsReadRaw(2, skip, sizeof skip, deadline)) return false;
  }
}

// One HTTP response: headers, then body (Content-Length / chunked /
// read-to-close). Returns the HTTP status (0 = transport failure).
static int tlsReadResponse(uint32_t deadline, bool *reusable) {
  *reusable = false;
  respCarryLen = 0;
  if (!tlsReadUntil("\r\n\r\n", respHdr, sizeof respHdr, deadline)) {
    // Diagnostics for the accepted-but-unanswered case (48 h soak reads
    // this): how much plaintext we actually got, and mbedtls's REAL last
    // error. diagSslErr is connect-time only and is stale here, so ask the
    // client directly.
    char emsg[48] = {0};
    int ecode = tlsClient.lastError(emsg, sizeof emsg);
    Serial.printf("[TLS ] no response headers: got=%d bytes '%.*s' "
                  "avail=%d conn=%d mbedtls_err=%d '%s'\n",
                  (int)strlen(respHdr), (int)(strlen(respHdr) > 48 ? 48
                                                                : strlen(respHdr)),
                  respHdr, tlsClient.available(), tlsClient.connected(), ecode,
                  emsg[0] ? emsg : "-");
    return 0;
  }
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
    ok = true;                       // read until close; never reused
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
  }
  if (!ok) return 0;
  respBody[stored] = '\0';
  if (strstr(respHdr, "Connection: close")) *reusable = false;
  return status;
}

// ============================ INGEST POST ==================================
static const char *metaReason() {
  if (camDegraded) return "camera-degraded";
  if (revoked401)  return "401-hold";
  return "";
}

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

// One multipart POST: head + meta [+ frame in 6 KiB chunks] + tail, then the
// small JSON response. Returns HTTP status; 0 = transport failure.
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

  bool reused = false;
  uint32_t connectMs = 0;
  if (!tlsEnsureConnected(&reused, &connectMs)) {
    tlsFail++;
    postFail++;
    if (!diagTcpOk) {
      Serial.printf("[TLS ] connect %lu ms FAILED | tcp probe %lu ms FAILED "
                    "(no SYN-ACK: route/PATH) heap %u\n",
                    (unsigned long)connectMs, (unsigned long)diagTcpMs,
                    (unsigned)ESP.getFreeHeap());
    } else {
      Serial.printf("[TLS ] connect %lu ms FAILED | tcp probe %lu ms OK | "
                    "handshake FAILED (ssl_err=%d '%s') heap %u\n",
                    (unsigned long)connectMs, (unsigned long)diagTcpMs,
                    diagSslErr, diagSslMsg, (unsigned)ESP.getFreeHeap());
    }
    return 0;
  }
  uint32_t t0 = millis();
  uint32_t deadline = t0 + POST_TIMEOUT_MS;

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
    Serial.printf("[TLS ] connect %lu ms (reuse=%d) | write FAILED after %lu ms "
                  "(heap %u)\n",
                  (unsigned long)(reused ? 0 : connectMs), (int)reused,
                  (unsigned long)writeMs, (unsigned)ESP.getFreeHeap());
    return 0;
  }

  uint32_t tr = millis();
  bool reusable = false;
  int status = tlsReadResponse(deadline, &reusable);
  uint32_t respMs = millis() - tr;
  if (status == 0) {
    postFail++;
    tlsClient.stop();
    Serial.printf("[TLS ] connect %lu ms (reuse=%d) | write %lu ms | response "
                  "FAILED after %lu ms (heap %u)\n",
                  (unsigned long)(reused ? 0 : connectMs), (int)reused,
                  (unsigned long)writeMs, (unsigned long)respMs,
                  (unsigned)ESP.getFreeHeap());
    return 0;
  }
  if (reusable) tlsLastUseMs = millis();
  else tlsClient.stop();

  // TLS spike print (PLAN §5): connect/write/response timings + heap per tick.
  Serial.printf("[TLS ] conn %lu ms%s | write %lu ms | resp %lu ms | "
                "total %lu ms | free heap %u\n",
                (unsigned long)(reused ? 0 : connectMs),
                reused ? " (idle-reuse)" : "",
                (unsigned long)writeMs, (unsigned long)respMs,
                (unsigned long)(millis() - t0), (unsigned)ESP.getFreeHeap());
  return status;
}

// ============================ RESPONSE HANDLING ============================
// Apply desired_cfg (apply-if-found: 201/409/422/429 carry it). Cadence keys
// live in "cam"/interval, sensor keys in the scfg blob. Ver is echoed as
// cfg_ver_applied on the next POST (read-back contract).
//
// Sensor keys are table-driven: one row per key carrying its clamp range, so
// parse/compare/persist is written once and a new key is a one-line addition.
struct CfgU8Row { const char *key; uint8_t SensorCfg::*m; uint32_t lo, hi; };
struct CfgI8Row { const char *key; int8_t  SensorCfg::*m; int32_t lo, hi; };

static const CfgU8Row CFG_U8[] = {
  { "\"quality\":",    &SensorCfg::quality,    4, 63 },   // JPEG 4..63
  { "\"framesize\":",  &SensorCfg::framesize,  0, FRAMESIZE_MAX_OV2640 },
  { "\"denoise\":",    &SensorCfg::denoise,    0, 1 },
  { "\"special_fx\":", &SensorCfg::special_fx, 0, 6 },
  { "\"wb_mode\":",    &SensorCfg::wb_mode,    0, 4 },
  { "\"awb\":",        &SensorCfg::awb,        0, 1 },
  { "\"aec\":",        &SensorCfg::aec,        0, 1 },
  { "\"agc\":",        &SensorCfg::agc,        0, 1 },
  { "\"agc_gain\":",   &SensorCfg::agc_gain,   0, 30 },
  { "\"hmirror\":",    &SensorCfg::hmirror,    0, 1 },
  { "\"vflip\":",      &SensorCfg::vflip,      0, 1 },
  { "\"dcw\":",        &SensorCfg::dcw,        0, 1 },
  { "\"lenc\":",       &SensorCfg::lenc,       0, 1 },
  { "\"colorbar\":",   &SensorCfg::colorbar,   0, 1 },
  { "\"led_pwm\":",    &SensorCfg::led_pwm,    0, 255 },
};
static const CfgI8Row CFG_I8[] = {
  { "\"brightness\":", &SensorCfg::brightness, -2, 2 },
  { "\"contrast\":",   &SensorCfg::contrast,   -2, 2 },
  { "\"saturation\":", &SensorCfg::saturation, -2, 2 },
  { "\"sharpness\":",  &SensorCfg::sharpness,  -2, 2 },
  { "\"ae_level\":",   &SensorCfg::ae_level,   -2, 2 },
};

static void applyCfgFromBody() {
  bool any = false;
  bool sensorChanged = false;
  uint32_t uv;
  int32_t sv;

  if (jsonU32(respBody, "\"interval_s\":", &uv)) {
    uv = clampu(uv, INTERVAL_MIN_S, INTERVAL_MAX_S);
    if (uv != intervalS) {
      intervalS = uv;
      camPrefs.putULong("interval", uv);
    }
    any = true;
  }
  if (jsonU32(respBody, "\"ver\":", &uv)) {
    if (uv != cfgVerApplied) {
      cfgVerApplied = uv;
      provPrefs.putULong("cfgver", uv);
    }
    any = true;
  }

  // --- sensor plane: any subset of the OV2640 feature set -------------------
  for (const CfgU8Row &r : CFG_U8) {
    if (!jsonU32(respBody, r.key, &uv)) continue;
    uv = clampu(uv, r.lo, r.hi);
    any = true;
    if (uv != sc.*(r.m)) { sc.*(r.m) = (uint8_t)uv; sensorChanged = true; }
  }
  for (const CfgI8Row &r : CFG_I8) {
    if (!jsonS32(respBody, r.key, &sv)) continue;
    sv = clampi(sv, r.lo, r.hi);
    any = true;
    if (sv != sc.*(r.m)) { sc.*(r.m) = (int8_t)sv; sensorChanged = true; }
  }

  if (sensorChanged) {
    saveSensorCfg();
    applySensorCfg();
  }
  if (any)
    Serial.printf("[CFG ] applied ver=%lu interval_s=%lu quality=%u fs=%u "
                  "br=%d ct=%d sat=%d fx=%u wb=%u hm=%u vf=%u led=%u\n",
                  (unsigned long)cfgVerApplied, (unsigned long)intervalS,
                  sc.quality, sc.framesize, sc.brightness, sc.contrast,
                  sc.saturation, sc.special_fx, sc.wb_mode, sc.hmirror,
                  sc.vflip, sc.led_pwm);
}

static uint32_t respNextS() {
  uint32_t v;
  if (jsonU32(respBody, "\"next_s\":", &v) && v > 0)
    return clampu(v, 5, 3600);
  return intervalS;
}

static uint32_t retryAfterS() {
  const char *ra = strstr(respHdr, "Retry-After:");
  long v = ra ? strtol(ra + 12, nullptr, 10) : 0;
  if (v <= 0) v = 10;
  if (v > 3600) v = 3600;
  return (uint32_t)v;
}

// ============================ TICK =========================================
// One full tick: capture (unless revoked/degraded) → POST → handle response →
// schedule the next tick. No queue: the next tick always captures fresh.

// Service background work for ms instead of a bare delay(): a retry gap is
// 6 s and a failing tick far longer — the LAN cage, LED patterns and serial
// console must stay alive throughout.
static void serialPoll();   // defined in the serial console section below
static void serviceIdle(uint32_t ms) {
  uint32_t until = millis() + ms;
  while (!due(until)) {
    server.handleClient();
    serialPoll();
    ledService();
    delay(10);
  }
}

static void runTick() {
  nextCaptureId();

  bool haveFrame = false;
  uint8_t *frameCopy = nullptr;
  size_t frameLen = 0;

  if (!revoked401) {
    bool attempt = false;
    if (!camDegraded && camInited) {
      attempt = true;
    } else if (camDegraded && due(camRetryAt)) {
      esp_camera_deinit();           // safe when not inited; covers hung sensor
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
        // upload streams from our copy in 6 KiB chunks. A PSRAM alloc
        // failure is NOT camera death (W4/F3): a PSRAM-less board or an
        // exhausted heap fell into capture_fail → degraded mode with a
        // 30-min backoff while the sensor was perfectly healthy. DRAM
        // holds a QVGA frame easily.
        frameCopy = (uint8_t *)heap_caps_malloc(fb->len, MALLOC_CAP_SPIRAM);
        if (!frameCopy)
          frameCopy = (uint8_t *)heap_caps_malloc(fb->len, MALLOC_CAP_8BIT);
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
  }

  const char *reason = metaReason();
  Serial.printf("[TICK] #%lu id=%s %s reason=%s heap=%u\n",
                (unsigned long)captureSeq, captureId,
                haveFrame ? "frame" : "health-only",
                reason[0] ? reason : "-", (unsigned)ESP.getFreeHeap());

  // Transport failures get fresh chances inside the same tick (marginal
  // 2.4 GHz drops packets for seconds); drop the frame when all fail.
  int status = 0;
  for (uint8_t attempt = 1; attempt <= POST_ATTEMPTS && status == 0; attempt++) {
    if (attempt > 1) {
      Serial.printf("[HTTP] retry attempt %u/%u\n", attempt, POST_ATTEMPTS);
      serviceIdle(POST_RETRY_GAP_MS);
    }
    status = ingestPost(haveFrame, frameCopy, frameLen);
  }
  if (frameCopy) free(frameCopy);

  // --- response contract (proto-v1) ----------------------------------------
  uint32_t sleepS = intervalS;
  if (status == 0) {
    lastHttp = 0;
    sleepS = intervalS;
    Serial.printf("[HTTP] transport failure after %u attempts -> frame dropped "
                  "(post_fail=%lu)\n", POST_ATTEMPTS, (unsigned long)postFail);
  } else {
    lastHttp = status;
    applyCfgFromBody();
    if (status == 201 || status == 409) {
      if (revoked401) {
        revoked401 = false;
        revokedBackoffS = REVOKED_BACKOFF_MIN;
        Serial.println("[HTTP] token accepted again -> normal cadence");
      }
      ledOkFlash();
      sleepS = respNextS();
      Serial.printf("[HTTP] %d ok -> sleep %lu s\n", status,
                    (unsigned long)sleepS);
    } else if (status == 401) {
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
    } else if (status == 429) {
      sleepS = retryAfterS();
      Serial.printf("[HTTP] 429 rate-limited -> sleep %lu s\n",
                    (unsigned long)sleepS);
    } else if (status == 422 || status == 413) {
      char why[48] = {0};
      // The key includes the value's OPENING quote — jsonStr's contract
      // (W4/B9): passing "\"reason\":" without it made strchr find that
      // same quote and copy an empty string, so the server's reject reason
      // always printed as "?".
      jsonStr(respBody, "\"reason\":\"", why, sizeof why);
      sleepS = respNextS();
      Serial.printf("[HTTP] %d rejected (%s) -> not fatal, sleep %lu s\n",
                    status, why[0] ? why : "?", (unsigned long)sleepS);
    } else {                           // 507 / 5xx: retry next tick
      postFail++;
      // Honor Retry-After when the server sends one (a full disk wants us
      // slow, not hammering); otherwise the configured cadence.
      sleepS = strstr(respHdr, "Retry-After:") ? retryAfterS() : intervalS;
      Serial.printf("[HTTP] %d server-side failure -> retry in %lu s "
                    "(post_fail=%lu)\n", status, (unsigned long)sleepS,
                    (unsigned long)postFail);
    }
  }

  nextTickAt = millis() + sleepS * 1000UL;
  Serial.printf("[TICK] next in %lu s (interval_s=%lu, cfg_ver=%lu, heap=%u)\n",
                (unsigned long)sleepS, (unsigned long)intervalS,
                (unsigned long)cfgVerApplied, (unsigned)ESP.getFreeHeap());
}

// Gate: run a tick only when due AND the link is up (never capture into void).
static void tickService() {
  if (!due(nextTickAt)) return;
  if (WiFi.status() != WL_CONNECTED) return;
  runTick();
}

// ============================ LAN CAGE (port 80) ===========================
// Installer-facing only: /api/status, /jpg (rate-limited), /ping and the
// caged factory reset. No stream, no settings form, no files, no OTA page.
static void buildStatusJson(char *j, size_t cap) {
  const char *camState = camDegraded ? (camEverOk ? "degraded" : "init-failed")
                                     : "ok";
  uint32_t nextIn = due(nextTickAt) ? 0 : (nextTickAt - millis()) / 1000;
  snprintf(j, cap,
           "{\"hostname\":\"%s\",\"ip\":\"%s\",\"rssi\":%d,\"wifi\":%s,"
           "\"free_heap\":%u,\"free_psram\":%u,\"uptime_s\":%lu,"
           "\"fw\":\"%s\",\"serial\":\"%s\","
           "\"boot\":%lu,\"brownout\":%lu,\"join_fail\":%lu,\"post_fail\":%lu,"
           "\"tls_fail\":%lu,\"capture_fail\":%lu,\"capture_seq\":%lu,"
           "\"cfg_ver_applied\":%lu,\"interval_s\":%lu,"
           "\"next_tick_in_s\":%lu,\"camera\":\"%s\",\"mode\":\"%s\","
           "\"last_http\":%d,"
           "\"cfg\":{\"quality\":%u,\"framesize\":%u,\"brightness\":%d,"
           "\"contrast\":%d,\"saturation\":%d,\"sharpness\":%d,"
           "\"denoise\":%u,\"special_fx\":%u,\"wb_mode\":%u,\"awb\":%u,"
           "\"aec\":%u,\"agc\":%u,\"ae_level\":%d,\"agc_gain\":%u,"
           "\"hmirror\":%u,\"vflip\":%u,\"dcw\":%u,\"lenc\":%u,"
           "\"colorbar\":%u,\"led_pwm\":%u}}",
           mdnsName, WiFi.localIP().toString().c_str(), WiFi.RSSI(),
           WiFi.status() == WL_CONNECTED ? "true" : "false",
           (unsigned)ESP.getFreeHeap(),
           (unsigned)(psramFound() ? ESP.getFreePsram() : 0),
           (unsigned long)(millis() / 1000UL), FW_VERSION, serialNo,
           (unsigned long)bootCount, (unsigned long)brownoutCount,
           (unsigned long)joinFail, (unsigned long)postFail,
           (unsigned long)tlsFail, (unsigned long)captureFail,
           (unsigned long)captureSeq, (unsigned long)cfgVerApplied,
           (unsigned long)intervalS, (unsigned long)nextIn,
           camState, revoked401 ? "revoked-401" : "normal", lastHttp,
           sc.quality, sc.framesize, sc.brightness, sc.contrast,
           sc.saturation, sc.sharpness, sc.denoise, sc.special_fx,
           sc.wb_mode, sc.awb, sc.aec, sc.agc, sc.ae_level, sc.agc_gain,
           sc.hmirror, sc.vflip, sc.dcw, sc.lenc, sc.colorbar, sc.led_pwm);
}

static void handleStatus() {
  static char j[2048];
  buildStatusJson(j, sizeof j);
  server.send(200, "application/json", j);
}

// One still per 3 s max; 503 while the camera is not inited.
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

// mDNS: guzva-<serial>.local (serial = efuse MAC), advertises http/80.
static void startMdns() {
  if (MDNS.begin(mdnsName)) {
    MDNS.addService("http", "tcp", 80);
    Serial.printf("[MDNS] http://%s.local\n", mdnsName);
  } else {
    Serial.println("[MDNS] begin failed");
  }
}

// ============================ SERIAL CONSOLE ===============================
// Reachability probe (PLAN install gate: ping <200 ms, RSSI ≥ −65). Tests
// the LAN leg (this PC), the gateway and two WAN targets — answers "can the
// board reach the internet at all" without guessing.
static void netProbe(const char *label, const char *host, uint16_t port,
                     uint32_t toMs) {
  WiFiClient c;
  uint32_t t = millis();
  bool ok = c.connect(host, port, (int32_t)toMs) != 0;
  uint32_t ms = millis() - t;
  if (ok) c.stop();
  Serial.printf("[NET ] %-11s %s:%u -> %s (%lu ms)\n", label, host,
                (unsigned)port, ok ? "OK" : "FAIL", (unsigned long)ms);
}

static void cmdNet() {
  Serial.printf("[NET ] ip=%s gw=%s mask=%s dns=%s rssi=%d status=%d\n",
                WiFi.localIP().toString().c_str(),
                WiFi.gatewayIP().toString().c_str(),
                WiFi.subnetMask().toString().c_str(),
                WiFi.dnsIP().toString().c_str(),
                (int)WiFi.RSSI(), (int)WiFi.status());
  if (WiFi.status() != WL_CONNECTED) {
    Serial.println("[NET ] wifi not connected");
    return;
  }
  netProbe("lan-pc", LAN_PROBE_HOST, 8080, 3000);
  netProbe("gw-http", WiFi.gatewayIP().toString().c_str(), 80, 3000);
  netProbe("wan-ingest", INGEST_HOST, INGEST_PORT, 6000);
  netProbe("wan-cf", "1.1.1.1", 443, 6000);
}

// USB provisioning path: "reset" clears 'prov' + 'cam' and restarts;
// "status" prints the LAN-cage JSON, "cfg" the sensor config, "net" the
// reachability probe.
static void handleSerialCmd(char *cmd) {
  if (strcmp(cmd, "reset") == 0) {
    factoryReset();
  } else if (strcmp(cmd, "net") == 0) {
    cmdNet();
  } else if (strcmp(cmd, "status") == 0) {
    static char j[2048];
    buildStatusJson(j, sizeof j);
    Serial.println(j);
  } else if (strcmp(cmd, "cfg") == 0) {
    Serial.printf("[CFG ] ver=%lu interval_s=%lu quality=%u framesize=%u "
                  "brightness=%d contrast=%d saturation=%d sharpness=%d "
                  "denoise=%u special_fx=%u wb_mode=%u awb=%u aec=%u agc=%u "
                  "ae_level=%d agc_gain=%u hmirror=%u vflip=%u dcw=%u "
                  "lenc=%u colorbar=%u led_pwm=%u\n",
                  (unsigned long)cfgVerApplied, (unsigned long)intervalS,
                  sc.quality, sc.framesize, sc.brightness, sc.contrast,
                  sc.saturation, sc.sharpness, sc.denoise, sc.special_fx,
                  sc.wb_mode, sc.awb, sc.aec, sc.agc, sc.ae_level,
                  sc.agc_gain, sc.hmirror, sc.vflip, sc.dcw, sc.lenc,
                  sc.colorbar, sc.led_pwm);
  } else {
    Serial.printf("[CLI] unknown command '%s' (try: reset, status, cfg, net)\n",
                  cmd);
  }
}

static void serialPoll() {
  static char line[64];   // W4: was 32 — overflow chars were dropped silently
                          // and a mangled command just didn't match anything
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
// Order: serial → LED → identity (efuse MAC) → NVS → camera (never fatal)
// → flash LEDC → TLS budget → WiFi (begin once) → LAN cage → mDNS → schedule.
// WiFi MUST precede the LAN cage: WiFi.mode()/begin() starts the lwIP tcpip
// thread, and WebServer.begin() creates a socket — doing that first trips
// `assert(sys_mbox_valid)` ("Invalid mbox") and reboot-loops.
// No NTP anywhere: capture never waits for time.
void setup() {
  Serial.begin(115200);
  delay(100);
  Serial.println();
  Serial.println("=== guzva firmware " FW_VERSION " ===");

  // Watchdog backstop (W4/F1, see WDT_TIMEOUT_S above). The IDF startup
  // code has ALREADY initialized the task WDT (arduino-esp32 2.x sdkconfig:
  // 5 s, watching IDLE0) — a second esp_task_wdt_init would return
  // INVALID_STATE and leave that 5 s watchdog live, and subscribing loopTask
  // to IT would reboot-loop on every long tick. So: deconfigure the
  // default, install ours, then subscribe THIS task (loopTask). loop()
  // feeds it every pass; the long blocking paths (tick, serviceIdle) all
  // finish well inside the budget, so a fire means a genuine stall.
  esp_task_wdt_deinit();
  esp_task_wdt_init(WDT_TIMEOUT_S, true);   // panic → reboot, bootCount++ visible server-side
  esp_task_wdt_add(nullptr);

  pinMode(INDICATOR_LED_PIN, OUTPUT);
  ledWrite(false);

  uint64_t mac = ESP.getEfuseMac();
  snprintf(serialNo, sizeof serialNo, "%06x", (uint32_t)(mac & 0xFFFFFF));
  snprintf(mdnsName, sizeof mdnsName, "%s-%s", MDNS_HOST_PREFIX, serialNo);

  loadNvs();

  // Flash LED PWM BEFORE the camera: initCamera() → applySensorCfg() writes
  // the LED duty, and LEDC must be set up by then (ch1; XCLK owns ch0).
  ledcSetup(FLASH_LEDC_CH, FLASH_LEDC_FREQ, FLASH_LEDC_BITS);
  ledcAttachPin(FLASH_GPIO_NUM, FLASH_LEDC_CH);
  ledcWrite(FLASH_LEDC_CH, sc.led_pwm);

  camInited = initCamera();
  if (camInited) {
    camEverOk = true;
  } else {
    camEnterDegraded();              // camera failure is NOT fatal
  }

  tlsClient.setHandshakeTimeout(TLS_HANDSHAKE_S);
#if INSECURE_TLS
  tlsClient.setInsecure();  // DEV ONLY — prod grep-gates INSECURE_TLS=0
#endif

  startWiFi();      // first: brings up the lwIP tcpip thread
  startWebServer(); // LAN cage — needs sockets, so after WiFi
  startMdns();

  nextTickAt = millis() + 5000;      // first beat after 5 s

  Serial.printf("[BOOT] serial=%s boot=%lu brownout=%lu cfg_ver=%lu "
                "interval_s=%lu\n",
                serialNo, (unsigned long)bootCount,
                (unsigned long)brownoutCount, (unsigned long)cfgVerApplied,
                (unsigned long)intervalS);
  // Never print the token itself — only its presence/length (secret).
  Serial.printf("[BOOT] PSRAM=%s free_heap=%u camera=%s token_len=%u wdt=%lus\n",
                psramFound() ? "yes" : "no", (unsigned)ESP.getFreeHeap(),
                camInited ? "ok" : "DEGRADED",
                (unsigned)strlen(deviceToken), (unsigned long)WDT_TIMEOUT_S);
}

void loop() {
  esp_task_wdt_reset();          // feed the backstop once per pass (W4/F1)
  serviceWiFi();                 // non-blocking reconnect with backoff
  server.handleClient();         // LAN cage
  tickService();                 // capture + HTTPS ingest when due
  serialPoll();                  // "reset" / "status" / "cfg" over USB
  ledService();                  // millis()-based LED patterns
  if (pendingFactoryAt != 0 && due(pendingFactoryAt)) factoryReset();
  delay(2);                      // yield — not a wait for anything
}
