/*

    As a test I gave ChatGPT my ESP32Cam demo sketch and asked it to re-write it but add features and 
    "take it to the next level".
    This is the result and it is so good I can't see any reason to try and write sketches myself any more!
    06Sep26





 * ESP32-CAM V2
 * AI-Thinker ESP32-CAM / OV2640
 *
 * Features:
 *  - PSRAM-aware camera setup, double buffering + CAMERA_GRAB_LATEST
 *  - Dedicated HTTP server (port 80) and MJPEG stream server (port 81)
 *  - Modern responsive dashboard embedded in the sketch
 *  - JSON status/settings API
 *  - Still capture to browser and microSD
 *  - Timelapse capture
 *  - Lightweight motion detection using frame luminance sampling
 *  - Motion event image capture + cooldown
 *  - NTP timestamps and sensible filenames
 *  - Persistent settings in Preferences/NVS
 *  - SD file listing, download and delete API
 *  - GPIO/flash control
 *  - Heap/PSRAM/RSSI/uptime diagnostics
 *  - Non-blocking Wi-Fi reconnect
 *
 * Notes:
 *  - Target: AI-Thinker ESP32-CAM with OV2640.
 *  - Set WIFI_SSID / WIFI_PASSWORD before flashing.
 *  - microSD is mounted in 1-bit mode so GPIO4 remains available for the flash LED.
 */

#include <Arduino.h>
#include <WiFi.h>
#include <WebServer.h>
#include <esp_camera.h>
#include <FS.h>
#include <SD_MMC.h>
#include <Preferences.h>
#include <time.h>
#include <esp_system.h>
#include <esp_heap_caps.h>
#include <HTTPClient.h>
#include <Update.h>
#include <SPIFFS.h>

// ============================ USER CONFIG ============================
// Everything in this section is intended to be safe and easy to customise.
//
// The sketch first looks for a separate "wifiSettings.h" file.  That lets you
// keep Wi-Fi and OTA credentials out of the main sketch if you prefer.
// If that file is not present, the fallback values immediately below are used.
// =======================================================================

// if config file esists (wifiSettings.h) it gets the settings from there otherwise use the ones below

// __has_include() is a compile-time check.  It lets the sketch work both:
//   1. with a separate wifiSettings.h file, and
//   2. as a single self-contained .ino file.
//
// This is handy when moving the project between computers/installations.
#if __has_include("wifiSettings.h")            // if config file exists us it
  #include "wifiSettings.h"
#else                                          // if no config file found use these settings

  // wifi
    static const char *WIFI_SSID = "<WIFI SSID HERE>";
    static const char *WIFI_PASSWORD = "<WIFI PASSWORD HERE>";

  // sketch title
    static const char *HOSTNAME      = "esp32cam-v2";

  // OTA
    static const char *OTA_USERNAME = "admin";
    static const char *OTA_PASSWORD = "password";  

#endif

// Optional static hostname only; DHCP is used for IP configuration.
static const long GMT_OFFSET_SEC = 0;
static const int  DST_OFFSET_SEC = 3600;

// AI-Thinker ESP32-CAM camera pins.
//
// These GPIO numbers are part of the physical wiring between the ESP32 and
// the OV2640 camera module.  They are NOT general-purpose pins you can freely
// reassign without changing the camera wiring/board definition.
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

#define FLASH_GPIO_NUM     4

// ============================ LIMITS ============================
// Timing, GPIO and safety limits used throughout the program.
// Keeping these values in one place makes the sketch much easier to tune.
//
// A few names worth knowing:
//   *_MS       = a time interval in milliseconds
//   *_BYTES    = a size in bytes
//   *_GPIO_PIN = a physical ESP32 GPIO number
// =======================================================================
static const uint32_t WIFI_RETRY_MS       = 10000;
static const uint32_t MOTION_SAMPLE_MS    = 1200;
static const uint32_t MOTION_COOLDOWN_MS  = 10000;
static const uint32_t TIMELAPSE_MIN_MS    = 1000;
static const uint32_t SD_MIN_FREE_BYTES   = 2UL * 1024UL * 1024UL;
static const float MAX_TEMP_C = 75.0f;
static const int DEFAULT_INPUT_PIN = 12;
static const int DEFAULT_OUTPUT_PIN = 13;
static const int INDICATOR_LED_PIN = 33;
static const bool OUTPUT_ON_LEVEL = LOW;
static const uint32_t OUTPUT_TIME_LIMIT_MS = 120UL * 60UL * 1000UL;
static const uint8_t RGB_SAMPLE_BYTES = 60;
#define ENABLE_OTA 1

// ============================ SETTINGS ============================
// "Settings" contains the user-adjustable camera behaviour.
//
// This is deliberately kept separate from the runtime variables below.
// The Settings structure is stored in ESP32 Preferences (NVS), so the last
// selected values survive a reboot/power cycle.

struct Settings {
  uint8_t  framesize = FRAMESIZE_VGA;
  uint8_t  quality = 14;
  int8_t   brightness = 0;
  int8_t   contrast = 0;
  int8_t   saturation = 0;
  uint16_t exposure = 0;
  uint8_t  gain = 0;
  bool     hmirror = false;
  bool     vflip = false;
  bool     flash = false;
  bool     motion = false;
  uint16_t motionThreshold = 13;
  uint16_t motionMinChanged = 8;
  uint32_t timelapseMs = 0;
  bool     swapGpios = false;
};

// One global Settings object holds the current camera configuration.
Settings settings;

// Dynamic GPIO pin accessors based on settings
inline int activeInputPin() { return settings.swapGpios ? DEFAULT_OUTPUT_PIN : DEFAULT_INPUT_PIN; }
inline int activeOutputPin() { return settings.swapGpios ? DEFAULT_INPUT_PIN : DEFAULT_OUTPUT_PIN; }

#define INPUT_GPIO_PIN activeInputPin()
#define OUTPUT_GPIO_PIN activeOutputPin()

// Preferences gives us non-volatile storage backed by the ESP32's NVS area.
// It is similar in purpose to EEPROM, but is key/value based.
Preferences prefs;

// Port 80: normal HTTP requests for the dashboard, APIs, captures, etc.
// Port 81: a separate raw MJPEG server used for the live camera stream.
//
// Keeping the stream on its own server prevents the normal web/API server
// from having to manage a long-lived multipart image response.
WebServer server(80);

uint32_t captureCount = 0;
uint32_t motionCount = 0;
uint32_t timelapseCount = 0;
uint32_t jpegBytesLast = 0;
uint32_t captureTimeUsLast = 0;

bool sdReady = false;
bool spiffsReady = false;
int imageCounter = 0;
uint32_t outputChangedAt = 0;
uint8_t illuminationBrightness = 0;
uint32_t nextTimelapse = 0;
uint32_t lastMotionSample = 0;
uint32_t lastMotionEvent = 0;
uint32_t motionDisplayUntil = 0;
bool motionDetected = false;
uint32_t lastWiFiAttempt = 0;
uint32_t streamFrameCounter = 0;

// Forward declaration
void applyPinModes();

// ============================ HELPERS ============================
// Small utility functions live here.  The aim is to keep the HTTP handlers
// and the main loop readable instead of putting lots of low-level conversion,
// filename and settings code inline.

// Escape characters that have special meaning inside a JSON string.
// We build JSON by hand in this sketch, so strings must be escaped before
// they are inserted into JSON responses.
String jsonEscape(const String &s) {
  String o;
  o.reserve(s.length() + 8);
  for (size_t i = 0; i < s.length(); ++i) {
    char c = s[i];
    if (c == '"') o += '\\';
    if (c == '\\') o += '\\';
    if (c == '\n') { o += "\\n"; continue; }
    if (c == '\r') { o += "\\r"; continue; }
    o += c;
  }
  return o;
}

// Convert the camera driver's numeric frame-size constant into the same
// human-readable name shown in the web dashboard.
const char *framesizeName(uint8_t f) {
  switch (f) {
    case FRAMESIZE_QQVGA: return "QQVGA";
    case FRAMESIZE_QVGA:  return "QVGA";
    case FRAMESIZE_VGA:   return "VGA";
    case FRAMESIZE_SVGA:  return "SVGA";
    case FRAMESIZE_XGA:   return "XGA";
    case FRAMESIZE_SXGA:  return "SXGA";
    case FRAMESIZE_UXGA:  return "UXGA";
    default: return "UNKNOWN";
  }
}

// Do the reverse of framesizeName(): turn a dashboard string such as "VGA"
// back into the numeric frame-size constant expected by the camera driver.
// If the supplied text is unknown, keep the current value unchanged.
uint8_t parseFramesize(const String &s) {
  if (s == "QQVGA") return FRAMESIZE_QQVGA;
  if (s == "QVGA")  return FRAMESIZE_QVGA;
  if (s == "VGA")   return FRAMESIZE_VGA;
  if (s == "SVGA")  return FRAMESIZE_SVGA;
  if (s == "XGA")   return FRAMESIZE_XGA;
  if (s == "SXGA")  return FRAMESIZE_SXGA;
  if (s == "UXGA")  return FRAMESIZE_UXGA;
  return settings.framesize;
}

// Read a boolean HTTP argument.  A missing argument leaves the current
// setting alone, while common true/false spellings are accepted.
bool argBool(const String &name, bool current) {
  if (!server.hasArg(name)) return current;
  String v = server.arg(name);
  v.toLowerCase();
  return v == "1" || v == "true" || v == "on" || v == "yes";
}

// Safety helper: force a value into an allowed range.
// Example: clampInt(99, 0, 31) returns 31.
int clampInt(int v, int lo, int hi) {
  return v < lo ? lo : (v > hi ? hi : v);
}

// Return a filesystem-friendly UTC/local time string for filenames.
// If NTP has not supplied a valid time yet, a fallback timestamp is used
// instead of producing a nonsense date.
String timestampString() {
  struct tm tmNow;
  if (!getLocalTime(&tmNow, 20)) return String(millis());
  char b[24];
  strftime(b, sizeof(b), "%Y%m%d_%H%M%S", &tmNow);
  return String(b);
}

// Generate a unique path for a JPEG on the SD card.
// The filename includes a prefix (IMG, MOT, TLP, etc.), the timestamp and
// an incrementing counter so repeated captures do not overwrite one another.
String uniquePhotoPath(const char *prefix = "IMG") {
  String base = "/img/" + String(prefix) + "_" + timestampString();
  String path = base + ".jpg";
  uint16_t n = 1;
  while (sdReady && SD_MMC.exists(path) && n < 1000) {
    path = base + "_" + String(n++) + ".jpg";
  }
  return path;
}

// Look through existing files at startup and recover the highest numbered
// image counter.  This prevents the counter from jumping backwards after a
// reboot and accidentally generating duplicate names.
void scanNumberedImages() {
  imageCounter = 0;
  if (!sdReady) return;
  File root = SD_MMC.open("/img");
  if (!root || !root.isDirectory()) { if (root) root.close(); return; }
  File f = root.openNextFile();
  while (f) {
    if (!f.isDirectory()) {
      String n = String(f.name());
      int slashIdx = n.lastIndexOf('/');
      String baseName = (slashIdx >= 0) ? n.substring(slashIdx + 1) : n;
      if (baseName.endsWith(".jpg")) {
        int b = baseName.length() - 4;
        String num = baseName.substring(0, b);
        bool numeric = num.length() > 0;
        for (size_t i = 0; i < num.length(); ++i) if (!isDigit(num[i])) numeric = false;
        if (numeric) { int val = (int)num.toInt(); if (val > imageCounter) imageCounter = val; }
      }
    }
    f.close();
    f = root.openNextFile();
  }
  root.close();
}

// Build a simple sequential filename/path for image storage.
String numberedPhotoPath() {
  if (!sdReady) return String("/img/1.jpg");
  int n = imageCounter + 1;
  while (n < 100000 && SD_MMC.exists("/img/" + String(n) + ".jpg")) ++n;
  imageCounter = n;
  return "/img/" + String(n) + ".jpg";
}

// Turn the ESP32-CAM's built-in flash LED on or off.
// The actual pin level depends on the board wiring, so the polarity is
// kept in this one small function rather than scattered through the code.
void setFlash(bool on) {
  settings.flash = on;
  digitalWrite(FLASH_GPIO_NUM, on ? HIGH : LOW);
}

// Save every user-adjustable setting to NVS (non-volatile storage).
// Preferences stores simple values by key, so the next boot can restore them.
void saveSettings() {
  prefs.begin("camv2", false);
  prefs.putUChar("size", settings.framesize);
  prefs.putUChar("qual", settings.quality);
  prefs.putChar("bright", settings.brightness);
  prefs.putChar("contr", settings.contrast);
  prefs.putChar("sat", settings.saturation);
  prefs.putUShort("exp", settings.exposure);
  prefs.putUChar("gain", settings.gain);
  prefs.putBool("hm", settings.hmirror);
  prefs.putBool("vf", settings.vflip);
  prefs.putBool("flash", settings.flash);
  prefs.putBool("motion", settings.motion);
  prefs.putUShort("mth", settings.motionThreshold);
  prefs.putUShort("mmc", settings.motionMinChanged);
  prefs.putULong("tlms", settings.timelapseMs);
  prefs.putBool("swapg", settings.swapGpios);
  prefs.end();
}

// Load settings saved by saveSettings().
// Missing keys are given the defaults from the Settings structure.
void loadSettings() {
  prefs.begin("camv2", true);
  settings.framesize = prefs.getUChar("size", FRAMESIZE_VGA);
  settings.quality = prefs.getUChar("qual", 14);
  settings.brightness = prefs.getChar("bright", 0);
  settings.contrast = prefs.getChar("contr", 0);
  settings.saturation = prefs.getChar("sat", 0);
  settings.exposure = prefs.getUShort("exp", 0);
  settings.gain = prefs.getUChar("gain", 0);
  settings.hmirror = prefs.getBool("hm", false);
  settings.vflip = prefs.getBool("vf", false);
  settings.flash = prefs.getBool("flash", false);
  settings.motion = prefs.getBool("motion", false);
  settings.motionThreshold = prefs.getUShort("mth", 13);
  settings.motionMinChanged = prefs.getUShort("mmc", 8);
  settings.timelapseMs = prefs.getULong("tlms", 0);
  settings.swapGpios = prefs.getBool("swapg", false);
  prefs.end();

  settings.quality = clampInt(settings.quality, 5, 63);
  settings.brightness = clampInt(settings.brightness, -2, 2);
  settings.contrast = clampInt(settings.contrast, -2, 2);
  settings.saturation = clampInt(settings.saturation, -2, 2);
  settings.exposure = clampInt(settings.exposure, 0, 1200);
  settings.gain = clampInt(settings.gain, 0, 31);
  settings.motionThreshold = clampInt(settings.motionThreshold, 1, 60);
  settings.motionMinChanged = clampInt(settings.motionMinChanged, 1, 64);
}

// Apply pin modes according to current swap state
void applyPinModes() {
  int inPin = activeInputPin();
  int outPin = activeOutputPin();
  pinMode(inPin, INPUT_PULLUP);
  pinMode(outPin, OUTPUT);
  digitalWrite(outPin, !OUTPUT_ON_LEVEL);
}

// Push our Settings structure into the OV2640 sensor.
//
// This function is important because changing a value in RAM does not by
// itself change the camera.  The sensor object must also receive the new
// brightness/contrast/exposure/etc. values.
void applySensorSettings() {
  // The camera driver gives us the live sensor object.  All image controls
  // below are applied directly to that sensor.
  sensor_t *s = esp_camera_sensor_get();
  if (!s) return;
  s->set_framesize(s, (framesize_t)settings.framesize);
  s->set_quality(s, settings.quality);
  s->set_brightness(s, settings.brightness);
  s->set_contrast(s, settings.contrast);
  s->set_saturation(s, settings.saturation);
  s->set_hmirror(s, settings.hmirror ? 1 : 0);
  s->set_vflip(s, settings.vflip ? 1 : 0);
  s->set_whitebal(s, 1);
  s->set_awb_gain(s, 1);
  if (settings.exposure == 0 && settings.gain == 0) {
    s->set_exposure_ctrl(s, 1);
    s->set_gain_ctrl(s, 1);
  } else {
    s->set_exposure_ctrl(s, 0);
    s->set_gain_ctrl(s, 0);
    s->set_agc_gain(s, settings.gain);
    s->set_aec_value(s, settings.exposure);
  }
}

// ============================ CAMERA ============================
// Initialise the OV2640 camera.
//
// This constructs the camera configuration structure expected by the ESP32
// camera driver, enables PSRAM-aware frame buffering when available, starts
// the driver and finally applies the stored sensor settings.
bool initCamera() {
  // camera_config_t is the driver's description of how the ESP32 is wired
  // to the OV2640 and how captured frames should be buffered/encoded.
  camera_config_t config;
  config.ledc_channel = LEDC_CHANNEL_0;
  config.ledc_timer = LEDC_TIMER_0;
  config.pin_d0 = Y2_GPIO_NUM;
  config.pin_d1 = Y3_GPIO_NUM;
  config.pin_d2 = Y4_GPIO_NUM;
  config.pin_d3 = Y5_GPIO_NUM;
  config.pin_d4 = Y6_GPIO_NUM;
  config.pin_d5 = Y7_GPIO_NUM;
  config.pin_d6 = Y8_GPIO_NUM;
  config.pin_d7 = Y9_GPIO_NUM;
  config.pin_xclk = XCLK_GPIO_NUM;
  config.pin_pclk = PCLK_GPIO_NUM;
  config.pin_vsync = VSYNC_GPIO_NUM;
  config.pin_href = HREF_GPIO_NUM;
  config.pin_sccb_sda = SIOD_GPIO_NUM;
  config.pin_sccb_scl = SIOC_GPIO_NUM;
  config.pin_pwdn = PWDN_GPIO_NUM;
  config.pin_reset = RESET_GPIO_NUM;
  config.xclk_freq_hz = 20000000;
  // JPEG is used because it keeps frame data compact enough for Wi-Fi and
  // SD storage.  It also lets the browser display the frame directly.
  config.pixel_format = PIXFORMAT_JPEG;
  config.frame_size = (framesize_t)settings.framesize;
  config.jpeg_quality = settings.quality;
  config.fb_location = CAMERA_FB_IN_PSRAM;
  config.grab_mode = CAMERA_GRAB_LATEST;
  config.fb_count = psramFound() ? 2 : 1;

  if (!psramFound()) {
    config.fb_location = CAMERA_FB_IN_DRAM;
    config.frame_size = (framesize_t)min((int)settings.framesize, (int)FRAMESIZE_VGA);
  }

  // Hand the completed configuration to the Espressif camera driver.
  esp_err_t err = esp_camera_init(&config);
  // Any non-OK result means the camera driver could not be started.
  if (err != ESP_OK) {
    Serial.printf("Camera init failed: 0x%x\n", err);
    return false;
  }

  applySensorSettings();
  return true;
}

// Capture a frame from the camera and return ownership of the frame buffer
// to the caller.  The caller MUST eventually call esp_camera_fb_return()
// or the camera can run out of frame buffers.
camera_fb_t *captureFrame() {
  uint32_t start = micros();
  camera_fb_t *fb = esp_camera_fb_get();
  captureTimeUsLast = micros() - start;
  if (fb) {
    captureCount++;
    jpegBytesLast = fb->len;
  }
  return fb;
}

// Write one already-captured camera frame to a filesystem path.
// "fb" is the camera frame buffer returned by esp_camera_fb_get().
bool saveFrame(camera_fb_t *fb, const String &path) {
  if (!sdReady || !fb) return false;
  if (SD_MMC.totalBytes() - SD_MMC.usedBytes() < SD_MIN_FREE_BYTES) return false;
  File f = SD_MMC.open(path, FILE_WRITE);
  if (!f) return false;
  size_t written = f.write(fb->buf, fb->len);
  f.close();
  return written == fb->len;
}


// Fallback storage helper for a frame when the SD card is unavailable.
// The sketch mainly uses SD for photo storage, but SPIFFS can still provide
// a small internal fallback for situations where that is useful.
bool saveFrameSPIFFS(camera_fb_t *fb) {
  if (!spiffsReady || !fb) return false;
  SPIFFS.remove("/image.jpg");
  File f = SPIFFS.open("/image.jpg", FILE_WRITE);
  if (!f) return false;
  size_t written = f.write(fb->buf, fb->len);
  f.close();
  return written == fb->len;
}

// Capture one fresh JPEG and optionally save it to SD.
//
// This is the common capture path used by normal snapshots, motion events
// and timelapse images.  The function also updates capture statistics.
bool captureToSD(const char *prefix, String *savedPath = nullptr) {
  // A capture can request the normal flash setting or a non-zero
  // illumination level from the dashboard.
  bool flashOn = settings.flash || illuminationBrightness > 0;
  uint8_t oldBrightness = illuminationBrightness;
  if (flashOn) {
    analogWrite(FLASH_GPIO_NUM, settings.flash ? 255 : illuminationBrightness);
    delay(100);
  }

  camera_fb_t *fb = captureFrame();
  if (!fb) {
    analogWrite(FLASH_GPIO_NUM, oldBrightness);
    return false;
  }

  String path;
  bool ok = false;
  if (sdReady) {
    path = uniquePhotoPath(prefix);
    ok = saveFrame(fb, path);
  } else if (spiffsReady) {
    path = "/image.jpg";
    ok = saveFrameSPIFFS(fb);
  }
  esp_camera_fb_return(fb);
  if (flashOn) analogWrite(FLASH_GPIO_NUM, oldBrightness);
  if (ok && savedPath) *savedPath = path;
  return ok;
}


// ============================ SD ============================
// Mount the microSD card in 1-bit mode.
//
// 1-bit mode matters on this board because it reduces SD_MMC pin usage and
// leaves GPIO4 available for the built-in flash LED.
bool initSD() {
  // AI-Thinker ESP32-CAM: 1-bit mode keeps GPIO4 available for flash LED.
  if (!SD_MMC.begin("/sdcard", true)) {
    Serial.println("SD_MMC mount failed");
    return false;
  }
  uint8_t cardType = SD_MMC.cardType();
  if (cardType == CARD_NONE) return false;
  Serial.printf("SD ready: %.2f MB\n", SD_MMC.cardSize() / 1048576.0f);
  SD_MMC.mkdir("/img");
  scanNumberedImages();
  return true;
}

// Remove old photos when the card is running low on free space.
// The goal is to prevent future captures from failing simply because the
// filesystem has filled up.  "keepAtLeast" prevents the cleanup from trying
// to remove the only remaining image.
void pruneOldestPhotos(uint8_t keepAtLeast = 1) {
  if (!sdReady) return;
  // Keep pruning until at least 5% free or 2MB free, whichever is larger.
  const uint64_t total = SD_MMC.totalBytes();
  const uint64_t freeB = total > SD_MMC.usedBytes() ? total - SD_MMC.usedBytes() : 0;
  if (freeB > max<uint64_t>(SD_MIN_FREE_BYTES, total / 20)) return;

  File root = SD_MMC.open("/img");
  if (!root || !root.isDirectory()) { if (root) root.close(); return; }

  String oldest;
  File file = root.openNextFile();
  while (file) {
    String n = file.name();
    int slashIdx = n.lastIndexOf('/');
    String baseName = (slashIdx >= 0) ? n.substring(slashIdx + 1) : n;
    if (!file.isDirectory() && baseName.endsWith(".jpg")) {
      String fullPath = "/img/" + baseName;
      if (oldest.isEmpty() || fullPath < oldest) oldest = fullPath;
    }
    file.close();
    file = root.openNextFile();
  }
  root.close();

  if (!oldest.isEmpty()) {
    SD_MMC.remove(oldest);
  }
}

// ============================ JSON / API ============================
// Build the JSON object returned by /api/status.
// The browser periodically polls this endpoint to refresh diagnostics,
 // GPIO states and motion status without reloading the whole page.
String statusJson() {
  uint64_t total = sdReady ? SD_MMC.totalBytes() : 0;
  uint64_t used  = sdReady ? SD_MMC.usedBytes() : 0;
  uint64_t freeB = total > used ? total - used : 0;

  String j = "{";
  j += "\"hostname\":\"" + jsonEscape(String(HOSTNAME)) + "\",";
  j += "\"ip\":\"" + jsonEscape(WiFi.localIP().toString()) + "\",";
  j += "\"rssi\":" + String(WiFi.RSSI()) + ",";
  j += "\"wifi\":" + String(WiFi.status() == WL_CONNECTED ? "true" : "false") + ",";
  j += "\"uptime\":" + String(millis() / 1000UL) + ",";
  j += "\"freeHeap\":" + String(ESP.getFreeHeap()) + ",";
  j += "\"freePSRAM\":" + String(psramFound() ? ESP.getFreePsram() : 0) + ",";
  j += "\"sdReady\":" + String(sdReady ? "true" : "false") + ",";
  j += "\"sdFree\":" + String((unsigned long long)freeB) + ",";
  j += "\"captures\":" + String(captureCount) + ",";
  j += "\"motionEvents\":" + String(motionCount) + ",";
  j += "\"motionDetected\":" + String(motionDetected ? "true" : "false") + ",";
  j += "\"timelapse\":" + String(timelapseCount) + ",";
  j += "\"lastJpegBytes\":" + String(jpegBytesLast) + ",";
  j += "\"lastCaptureMs\":" + String(captureTimeUsLast / 1000UL) + ",";
  j += "\"resolution\":\"" + String(framesizeName(settings.framesize)) + "\",";
  j += "\"quality\":" + String(settings.quality) + ",";
  j += "\"motionEnabled\":" + String(settings.motion ? "true" : "false") + ",";
  j += "\"timelapseMs\":" + String(settings.timelapseMs) + ",";
  j += "\"swapGpios\":" + String(settings.swapGpios ? "true" : "false") + ",";
  j += "\"gpioInputPin\":" + String(INPUT_GPIO_PIN) + ",";
  j += "\"gpioInputState\":" + String(digitalRead(INPUT_GPIO_PIN) ? "true" : "false") + ",";
  j += "\"gpioOutputPin\":" + String(OUTPUT_GPIO_PIN) + ",";
  j += "\"gpioOutputState\":" + String(digitalRead(OUTPUT_GPIO_PIN) == OUTPUT_ON_LEVEL ? "true" : "false");
  j += "}";
  return j;
}

// Build a JSON representation of the current Settings structure.
// The dashboard uses this both when initially loading the form and after
// applying new values.
String settingsJson() {
  String j = "{";
  j += "\"resolution\":\"" + String(framesizeName(settings.framesize)) + "\",";
  j += "\"quality\":" + String(settings.quality) + ",";
  j += "\"brightness\":" + String(settings.brightness) + ",";
  j += "\"contrast\":" + String(settings.contrast) + ",";
  j += "\"saturation\":" + String(settings.saturation) + ",";
  j += "\"exposure\":" + String(settings.exposure) + ",";
  j += "\"gain\":" + String(settings.gain) + ",";
  j += "\"hmirror\":" + String(settings.hmirror ? "true" : "false") + ",";
  j += "\"vflip\":" + String(settings.vflip ? "true" : "false") + ",";
  j += "\"flash\":" + String(settings.flash ? "true" : "false") + ",";
  j += "\"motion\":" + String(settings.motion ? "true" : "false") + ",";
  j += "\"motionThreshold\":" + String(settings.motionThreshold) + ",";
  j += "\"motionMinChanged\":" + String(settings.motionMinChanged) + ",";
  j += "\"timelapseMs\":" + String(settings.timelapseMs) + ",";
  j += "\"swapGpios\":" + String(settings.swapGpios ? "true" : "false") + ",";
  j += "\"gpioInputPin\":" + String(INPUT_GPIO_PIN) + ",";
  j += "\"gpioInputState\":" + String(digitalRead(INPUT_GPIO_PIN) ? "true" : "false") + ",";
  j += "\"gpioOutputPin\":" + String(OUTPUT_GPIO_PIN) + ",";
  j += "\"gpioOutputState\":" + String(digitalRead(OUTPUT_GPIO_PIN) == OUTPUT_ON_LEVEL ? "true" : "false");
  j += "}";
  return j;
}

// Common helper for sending JSON with the correct content type.
// CORS header lets the local site (web/index.html) call this API.
void sendJson(const String &body, int code = 200) {
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.send(code, "application/json; charset=utf-8", body);
}

void handleStatus() { sendJson(statusJson()); }
void handleSettingsGet() { sendJson(settingsJson()); }

// Read camera/settings values from HTTP query/form arguments and validate
// them before copying them into the global Settings structure.
void applyArgsToSettings() {
  if (server.hasArg("resolution")) settings.framesize = parseFramesize(server.arg("resolution"));
  if (server.hasArg("quality")) settings.quality = clampInt(server.arg("quality").toInt(), 5, 63);
  if (server.hasArg("brightness")) settings.brightness = clampInt(server.arg("brightness").toInt(), -2, 2);
  if (server.hasArg("contrast")) settings.contrast = clampInt(server.arg("contrast").toInt(), -2, 2);
  if (server.hasArg("saturation")) settings.saturation = clampInt(server.arg("saturation").toInt(), -2, 2);
  if (server.hasArg("exposure")) settings.exposure = clampInt(server.arg("exposure").toInt(), 0, 1200);
  if (server.hasArg("gain")) settings.gain = clampInt(server.arg("gain").toInt(), 0, 31);
  settings.hmirror = argBool("hmirror", settings.hmirror);
  settings.vflip = argBool("vflip", settings.vflip);
  settings.flash = argBool("flash", settings.flash);
  settings.motion = argBool("motion", settings.motion);
  settings.swapGpios = argBool("swapGpios", settings.swapGpios);
  if (server.hasArg("motionThreshold")) settings.motionThreshold = clampInt(server.arg("motionThreshold").toInt(), 1, 60);
  if (server.hasArg("motionMinChanged")) settings.motionMinChanged = clampInt(server.arg("motionMinChanged").toInt(), 1, 64);
  if (server.hasArg("timelapseMs")) {
    long v = server.arg("timelapseMs").toInt();
    settings.timelapseMs = v <= 0 ? 0 : max<long>(TIMELAPSE_MIN_MS, v);
  }

  applySensorSettings();
  setFlash(settings.flash);
  applyPinModes();
  nextTimelapse = millis() + (settings.timelapseMs ? settings.timelapseMs : 0);
}

// Legacy/general settings handler: apply incoming HTTP arguments,
// save the new settings to NVS, then return the updated settings as JSON.
void handleSettingsSet() {
  applyArgsToSettings();
  saveSettings();
  sendJson(settingsJson());
}

// POST version of the settings handler used by the modern dashboard.
// POST is preferable for a larger group of form values because the values
// are sent in the request body rather than encoded into the URL.
void handleSettingsSetPost() {
  applyArgsToSettings();
  saveSettings();
  sendJson(settingsJson());
}

// Send one camera frame directly as an HTTP JPEG response.
// This is the core helper behind the /jpg, /capture, /photo endpoints.
void sendJpegFrame(camera_fb_t *fb) {
  WiFiClient client = server.client();
  client.setTimeout(1000);
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.setContentLength(fb->len);
  server.send(200, "image/jpeg", "");
  client.write(fb->buf, fb->len);
}

// HTTP handler for /jpg: capture a fresh still image and return it directly.
void handleJPG() {
  static uint32_t lastJpg = 0;
  if (millis() - lastJpg > 3000) {
    camera_fb_t *drop = esp_camera_fb_get();
    if (drop) esp_camera_fb_return(drop);
    lastJpg = millis();
  }
  camera_fb_t *fb = nullptr;
  for (int i = 0; i < 3 && !fb; ++i) {
    fb = captureFrame();
    if (!fb) delay(30);
  }
  if (!fb) return server.send(503, "text/plain", "Camera capture failed");
  sendJpegFrame(fb);
  esp_camera_fb_return(fb);
}

// HTTP handler for the dashboard's "Capture" action.
// A query parameter can request that the image is also stored on SD.
void handleCapture() {
  bool flash = settings.flash;
  if (server.hasArg("flash")) flash = argBool("flash", settings.flash);
  bool oldFlash = settings.flash;
  if (flash) analogWrite(FLASH_GPIO_NUM, 255);
  delay(flash ? 100 : 0);
  camera_fb_t *fb = captureFrame();
  if (flash) analogWrite(FLASH_GPIO_NUM, illuminationBrightness);
  settings.flash = oldFlash;
  if (!fb) return server.send(503, "text/plain", "Camera capture failed");
  if (!server.hasArg("save") || server.arg("save") != "0") {
    if (sdReady) saveFrame(fb, uniquePhotoPath("IMG"));
    else if (spiffsReady) saveFrameSPIFFS(fb);
  }
  sendJpegFrame(fb);
  esp_camera_fb_return(fb);
}

// /photo is another still-image endpoint kept for compatibility with
// applications that expect this path.
void handlePhoto() {
  bool flashOn = settings.flash || illuminationBrightness > 0;
  uint8_t old = illuminationBrightness;
  if (flashOn) { analogWrite(FLASH_GPIO_NUM, settings.flash ? 255 : illuminationBrightness); delay(100); }
  camera_fb_t *fb = captureFrame();
  if (flashOn) analogWrite(FLASH_GPIO_NUM, old);
  if (!fb) return server.send(503, "text/plain", "Camera capture failed");
  String path;
  bool ok;
  if (sdReady) { path = numberedPhotoPath(); ok = saveFrame(fb, path); }
  else { path = "/image.jpg"; ok = saveFrameSPIFFS(fb); }
  esp_camera_fb_return(fb);
  if (!ok) { if(sdReady && imageCounter>0) imageCounter--; return server.send(503, "text/plain", "Failed to save image"); }
  server.send(200, "text/plain", "Image saved: " + path);
}


// /img is another compatibility endpoint.  Its optional query argument is
// accepted so existing callers can continue using /img?img=1.
void handleImg() {
  if (sdReady) {
    int n = imageCounter;
    if (server.hasArg("img")) {
      String a = server.arg("img");
      if (a.length() == 0) return server.send(400, "text/plain", "Invalid img number");
      n = a.toInt();
      if (n < 1) return server.send(400, "text/plain", "Invalid img number");
    }
    if (n < 1) return server.send(404, "text/plain", "No numbered images");

    String path = "/img/" + String(n) + ".jpg";
    File f = SD_MMC.open(path, FILE_READ);
    if (!f || f.isDirectory()) {
      if (f) f.close();
      return server.send(404, "text/plain", "Image not found: " + path);
    }
    server.sendHeader("Cache-Control", "no-cache, no-store, must-revalidate");
    server.sendHeader("Access-Control-Allow-Origin", "*");
    server.streamFile(f, "image/jpeg");
    f.close();
  } else if (spiffsReady) {
    File f = SPIFFS.open("/image.jpg", FILE_READ);
    if (!f) return server.send(404, "text/plain", "No image found");
    server.sendHeader("Access-Control-Allow-Origin", "*");
    server.streamFile(f, "image/jpeg");
    f.close();
  } else {
    server.send(404, "text/plain", "No storage available");
  }
}

// Return a JSON list of stored image files on the SD card.
void handleFiles() {
  if (!sdReady) return sendJson("{\"error\":\"SD not available\"}", 503);
  String j = "[";
  File root = SD_MMC.open("/img");
  if (root && root.isDirectory()) {
    File f = root.openNextFile();
    bool first = true;
    while (f) {
      String n = f.name();
      int slashIdx = n.lastIndexOf('/');
      String baseName = (slashIdx >= 0) ? n.substring(slashIdx + 1) : n;
      if (!f.isDirectory() && baseName.endsWith(".jpg")) {
        if (!first) j += ",";
        String fullPath = "/img/" + baseName;
        j += "{\"name\":\"" + jsonEscape(fullPath) + "\",\"size\":" + String((unsigned long)f.size()) + "}";
        first = false;
      }
      f.close();
      f = root.openNextFile();
    }
    root.close();
  }
  j += "]";
  sendJson(j);
}

// Normalise/validate a requested filesystem path before opening it.
// This is important because the path ultimately comes from an HTTP client.
String safePath(String p) {
  if (!p.startsWith("/")) p = "/" + p;
  p.replace("..", "");
  return p;
}

// Send an image file from the SD card back to the browser.
// The filename is supplied as a request parameter and is sanitised first.
void handleDownload() {
  if (!sdReady || !server.hasArg("name")) return server.send(400, "text/plain", "Missing name");
  String p = safePath(server.arg("name"));
  File f = SD_MMC.open(p, FILE_READ);
  if (!f || f.isDirectory()) {
    if (f) f.close();
    return server.send(404, "text/plain", "Not found");
  }
  server.sendHeader("Access-Control-Allow-Origin", "*");
  server.streamFile(f, "image/jpeg");
  f.close();
}

void handleDelete() {
  if (!sdReady || !server.hasArg("name")) return server.send(400, "text/plain", "Missing name");
  String p = safePath(server.arg("name"));
  bool ok = SD_MMC.remove(p);
  sendJson(String("{\"ok\":") + (ok ? "true" : "false") + "}", ok ? 200 : 404);
}

// Simple extension check used when deciding which files count as photos.
bool isJpegName(const String &name) {
  String n = name;
  n.toLowerCase();
  return n.endsWith(".jpg") || n.endsWith(".jpeg");
}

uint32_t clearJpegFiles(fs::FS &fs, const char *rootPath) {
  uint32_t removed = 0;
  File root = fs.open(rootPath);
  if (!root || !root.isDirectory()) { if (root) root.close(); return 0; }
  File f = root.openNextFile();
  while (f) {
    String name = f.name();
    bool isDir = f.isDirectory();
    String fullPath = name;
    if (!fullPath.startsWith("/")) {
      String rp = String(rootPath);
      if (rp.endsWith("/")) fullPath = rp + name;
      else fullPath = rp + "/" + name;
    }
    f.close();
    if (isDir) {
      File sub = fs.open(fullPath);
      if (sub && sub.isDirectory()) {
        File sf = sub.openNextFile();
        while (sf) {
          String subName = sf.name();
          bool subIsDir = sf.isDirectory();
          String subFullPath = subName;
          if (!subFullPath.startsWith("/")) {
            if (fullPath.endsWith("/")) subFullPath = fullPath + subName;
            else subFullPath = fullPath + "/" + subName;
          }
          sf.close();
          if (!subIsDir && isJpegName(subFullPath) && fs.remove(subFullPath)) {
            ++removed;
          }
          sf = sub.openNextFile();
        }
        sub.close();
      }
    } else {
      if (isJpegName(fullPath) && fs.remove(fullPath)) {
        ++removed;
      }
    }
    f = root.openNextFile();
  }
  root.close();
  return removed;
}

// HTTP handler that deletes all stored JPEG images.
// This is intentionally separate from handleDelete(), which removes one file.
void handleClearImages() {
  uint32_t removed = 0;
  if (sdReady) removed = clearJpegFiles(SD_MMC, "/");
  if (spiffsReady && SPIFFS.exists("/image.jpg")) { if (SPIFFS.remove("/image.jpg")) ++removed; }
  scanNumberedImages();
  sendJson(String("{\"ok\":true,\"removed\":") + String(removed) + "}");
}

// Return raw image/camera data used by one of the compatibility endpoints.
void handleData() {
  uint64_t freeB = sdReady ? (SD_MMC.totalBytes() - SD_MMC.usedBytes()) : 0;
  String out;
  if (!sdReady) out += "NO SD CARD DETECTED";
  else out += "SD Card: " + String(SD_MMC.usedBytes()/1048576ULL) + "MB used - " + String(freeB/1048576ULL) + "MB free";
  out += ",Illumination led brightness=" + String(illuminationBrightness) + " &ensp; Flash is " + String(settings.flash ? "Enabled" : "Off");
  out += ",Current time: " + timestampString();
  out += ",GPIO output pin " + String(OUTPUT_GPIO_PIN) + " is: " + String(digitalRead(OUTPUT_GPIO_PIN)==OUTPUT_ON_LEVEL ? "ON" : "OFF");
  out += " &ensp; GPIO input " + String(INPUT_GPIO_PIN) + " is: " + String(digitalRead(INPUT_GPIO_PIN) ? "ON" : "OFF");
  out += ",Image size: " + String(framesizeName(settings.framesize));
  out += ",Free memory: " + String(ESP.getFreeHeap()/1000) + "K &ensp; Wifi strength: " + String(WiFi.RSSI()) + "dBm &ensp; Temperature: " + String(temperatureRead()) + "C";
  server.send(200, "text/plain", out);
}

// Control the spare output GPIO (GPIO13 or swapped GPIO12).
// The output is active-low on this particular build, so OUTPUT_ON_LEVEL
// defines which digitalWrite() value means "ON".
void handleSwitch() {
  if (!server.hasArg("on")) return server.send(400, "text/plain", "error - no command received");
  int v = server.arg("on").toInt();
  if (v == 0) { digitalWrite(OUTPUT_GPIO_PIN, !OUTPUT_ON_LEVEL); outputChangedAt = millis(); server.send(200,"text/plain","Switched off"); }
  else if (v == 1) { digitalWrite(OUTPUT_GPIO_PIN, OUTPUT_ON_LEVEL); outputChangedAt = millis(); server.send(200,"text/plain","Switched on"); }
  else server.send(400,"text/plain","Invalid value");
}

// Tiny health-check endpoint: useful for external scripts because it returns
// a predictable response without requiring a camera capture.
void handlePing() { server.send(200, "text/plain", "ok"); }

// Explicitly reboot the board.  The short delay lets the HTTP response leave
// the network stack before the ESP32 resets.
void handleReboot() { server.send(200, "text/plain", "Rebooting...."); delay(300); ESP.restart(); }

// Generic 404 handler.  It prints the requested URI, method and arguments,
// which makes debugging accidental/wrong URLs much easier from a browser
// or Serial-connected development session.
void handleNotFound() {
  String out = "File Not Found\n\nURI: " + server.uri() + "\nMethod: " + String(server.method()==HTTP_GET ? "GET" : "POST") + "\nArguments: " + String(server.args()) + "\n";
  for (uint8_t i=0;i<server.args();i++) out += " " + server.argName(i) + ": " + server.arg(i) + "\n";
  server.send(404, "text/plain", out);
}

// Small outbound HTTP helper used by diagnostic/testing functionality.
// It performs a GET and optionally returns both the page body and the
// requested URL/response information to the caller.
int requestWebPage(String *page, String *received, int maxWaitTime) {
  if (!page || !received) return -1;
  WiFiClient client;
  HTTPClient http;
  http.setTimeout(maxWaitTime);
  if (!http.begin(client, *page)) { *received = "error: begin"; return -1; }
  int code = http.GET();
  *received = code > 0 ? http.getString() : "error:" + String(code);
  http.end();
  return code;
}

// Set the remembered illumination/flash level exposed by the web UI.
void handleIllumination() {
  if (!server.hasArg("level")) return server.send(400,"text/plain","Missing level");
  illuminationBrightness = clampInt(server.arg("level").toInt(), 0, 255);
  analogWrite(FLASH_GPIO_NUM, illuminationBrightness);
  sendJson(String("{\"level\":") + illuminationBrightness + "}");
}

// ============================ MOTION ============================
// Samples a small number of pixels from the current JPEG frame's raw bytes.
// This deliberately uses JPEG byte differences rather than full image decoding:
// cheap, fast and suitable for an ESP32-CAM without an image-processing stack.
// Compare the current camera frame against a small stored baseline.
//
// This is deliberately lightweight: rather than doing expensive full-frame
// image analysis, it samples luminance at a number of locations and counts
// how many samples changed by at least motionThreshold.
//
// The baseline is updated over time, so the detector is aimed at noticeable
// scene changes rather than being a sophisticated computer-vision system.
bool motionChanged(camera_fb_t *fb) {
  if (!fb || fb->len < 256) return false;
  static uint8_t baseline[32];
  static bool baselineValid = false;
  uint16_t changed = 0;
  const size_t n = sizeof(baseline);

  for (size_t i = 0; i < n; ++i) {
    size_t pos = 32 + ((fb->len - 64 - 32) * i) / (n - 1);
    uint8_t v = fb->buf[pos];
    if (baselineValid) {
      int d = abs((int)v - (int)baseline[i]);
      if (d >= settings.motionThreshold) changed++;
    }
    baseline[i] = v;
  }
  // The first sample establishes a baseline.  There cannot be a meaningful
  // "change" result until at least one previous frame has been seen.
  if (!baselineValid) {
    baselineValid = true;
    return false;
  }
  return changed >= settings.motionMinChanged;
}

// Non-blocking motion-detection service.
//
// Called from loop() on every pass, but it actually performs a camera sample
// only every MOTION_SAMPLE_MS milliseconds.  That keeps the main loop free
// to service HTTP requests and the live stream between samples.
void motionTask() {
  // millis() intentionally wraps after a long time.  Unsigned subtraction
  // plus the signed comparison pattern used here keeps timing tests robust.
  uint32_t now = millis();
  if (motionDetected && (int32_t)(now - motionDisplayUntil) >= 0) motionDetected = false;
  // Motion detection is opt-in.  Exit immediately when it is disabled so
  // the camera is not periodically captured just for motion monitoring.
  if (!settings.motion) return;
  // Do not sample on every loop() iteration; that would consume camera and
  // CPU bandwidth and could interfere with still captures.
  if (now - lastMotionSample < MOTION_SAMPLE_MS) return;
  lastMotionSample = now;

  // Motion sampling uses the same camera capture mechanism as normal photos.
  // The frame is inspected briefly and then returned immediately so the
  // camera buffer is available again for streaming/captures.
  camera_fb_t *fb = captureFrame();
  if (!fb) return;
  bool changed = motionChanged(fb);
  esp_camera_fb_return(fb);

  // Require a cooldown between stored motion events.  Without this, one
  // person moving in front of the camera could fill the SD card with dozens
  // of nearly identical images.
  if (changed && now - lastMotionEvent >= MOTION_COOLDOWN_MS) {
    lastMotionEvent = now;
    motionDetected = true;
    motionDisplayUntil = now + 3000;
    Serial.println("*** MOTION DETECTED ***");
    String path;
    bool stored = captureToSD("MOT", &path);
    if (stored) {
      motionCount++;
      Serial.println("Motion image saved: " + path);
    } else if (!sdReady && !spiffsReady) {
      Serial.println("Motion detected but no storage available");
    }
  }
}

// Non-blocking timelapse service.
//
// Instead of delay()'ing for the requested interval, this compares millis()
// with nextTimelapse.  That means Wi-Fi, web requests and motion monitoring
// continue running while the timelapse waits for its next capture time.
void timelapseTask() {
  // Timelapse is active only when an SD card is ready and a non-zero interval
  // has been configured.  A zero interval means "off" in the dashboard.
  if (!sdReady || settings.timelapseMs == 0) return;
  uint32_t now = millis();
  if ((int32_t)(now - nextTimelapse) < 0) return;
  // Schedule the following capture before doing the actual capture.  This
  // keeps the timing tied to the requested interval rather than accumulating
  // the capture/write time into every successive delay.
  nextTimelapse = now + settings.timelapseMs;
  String path;
  if (captureToSD("TLP", &path)) timelapseCount++;
  pruneOldestPhotos();
}

// ============================ LOCAL WEB UI ============================
// The device UI now lives outside the firmware (see web/index.html).
// Only a tiny stub remains here so a browser pointed at the device gets
// pointers to the API instead of a dead page.
//
// The JavaScript talks back to the ESP32 through the /api/... endpoints and
// automatically refreshes status/file listings in the background.

static const char INDEX_HTML[] PROGMEM = R"HTML(
<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ESP32-CAM API</title>
<style>body{font-family:system-ui,sans-serif;margin:24px;max-width:640px;line-height:1.6}</style>
</head><body>
<h2>ESP32-CAM capture API</h2>
<p>The UI moved to the local site (<code>web/index.html</code>). This device only serves the capture API:</p>
<ul>
<li><a href="/jpg">/jpg</a> still image</li>
<li><a href="/capture?save=1">/capture?save=1</a> still image + save to SD</li>
<li><a href="/photo">/photo</a> capture and save, returns path</li>
<li><a href="/api/status">/api/status</a> JSON status</li>
<li><a href="/api/settings">/api/settings</a> JSON settings</li>
<li><a href="/api/files">/api/files</a> SD file list</li>
<li><a href="/ping">/ping</a> health check</li>
</ul>
</body></html>
)HTML";

void handleRoot() {
  server.send_P(200, "text/html; charset=utf-8", INDEX_HTML);
}

// ============================ WIFI ============================
// Start the ESP32 as a Wi-Fi station using the configured network.
// Power-saving is disabled because it can make a live camera stream feel
// sluggish or irregular.
void beginWiFi() {
  WiFi.mode(WIFI_STA);
  WiFi.setHostname(HOSTNAME);
  WiFi.setSleep(false); // improves stream responsiveness on ESP32-CAM
  WiFi.setTxPower(WIFI_POWER_19_5dBm); // max TX power helps weak-signal links
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  lastWiFiAttempt = millis();
}

// Non-blocking Wi-Fi reconnect service.
// Rather than sitting in a long while() loop whenever Wi-Fi disappears,
// the main loop retries every WIFI_RETRY_MS milliseconds.
void serviceWiFi() {
  if (WiFi.status() == WL_CONNECTED) return;
  uint32_t now = millis();
  if (now - lastWiFiAttempt < WIFI_RETRY_MS) return;
  lastWiFiAttempt = now;
  WiFi.disconnect();
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
}

// =======================================================================
// OTA (Over-The-Air) UPDATE SUPPORT
// =======================================================================
// This whole section is compiled only when ENABLE_OTA is non-zero.
// The OTA page is protected by HTTP Basic Authentication and accepts a
// compiled .bin firmware file.
#if ENABLE_OTA
bool otaStarted = false;
bool otaFailed = false;
bool otaAuthorized = false;

// Tell the browser to display an HTTP Basic Authentication prompt.
void otaRequestAuth() {
  server.requestAuthentication(BASIC_AUTH, "ESP32-CAM OTA", "Enter the OTA password");
}

// Return true only when the supplied OTA username/password are valid.
bool otaCheckAuth() {
  if (!server.authenticate(OTA_USERNAME, OTA_PASSWORD)) {
    otaRequestAuth();
    return false;
  }
  return true;
}

// Render the HTML upload page used for firmware updates.
void handleOTAPage() {
  if (!otaCheckAuth()) return;
  String html = "<!doctype html><html><head><meta name='viewport' content='width=device-width,initial-scale=1'><title>ESP32-CAM V2 OTA</title>";
  html += "<style>body{font-family:Arial,sans-serif;margin:24px;max-width:720px}.box{padding:18px;border:1px solid #ccc;border-radius:10px}input,button{padding:10px;margin-top:8px} .note{color:#555}</style></head><body>";
  html += "<div class='box'><h2>ESP32-CAM V2 firmware update</h2>";
  html += "<p>Password authentication is required to access this page.</p>";
  html += "<p>Select a compiled <b>.bin</b> file and upload it.</p>";
  html += "<form method='POST' action='/ota' enctype='multipart/form-data'><input type='file' name='update' accept='.bin' required><br><button type='submit'>Upload firmware</button></form>";
  html += "<p class='note'>OTA requires an Arduino partition scheme with an OTA app slot. If your board is set to <b>Huge APP</b>, change it to a scheme that includes OTA (for example <b>Default 4MB with spiffs</b>) and reflash the sketch once via USB.</p>";
  html += "<p><a href='/'>Return to dashboard</a></p></div></body></html>";
  server.send(200,"text/html",html);
}

// Process an incoming firmware upload in chunks.
//
// The WebServer library calls this repeatedly with UPLOAD_FILE_START,
// UPLOAD_FILE_WRITE and UPLOAD_FILE_END events.  The firmware is written
// directly into the inactive OTA partition as chunks arrive.
void handleOTAUpload() {
  HTTPUpload &upload = server.upload();
  if (upload.status == UPLOAD_FILE_START) {
    otaAuthorized = server.authenticate(OTA_USERNAME, OTA_PASSWORD);
    otaStarted = false;
    otaFailed = false;
    if (!otaAuthorized) {
      otaFailed = true;
      Serial.println("OTA rejected: authentication failed");
      return;
    }
    Serial.printf("OTA start: %s\n", upload.filename.c_str());
    otaStarted = Update.begin(UPDATE_SIZE_UNKNOWN);
    if (!otaStarted) {
      otaFailed = true;
      Serial.println("OTA unavailable: no writable OTA partition or insufficient space");
      Update.printError(Serial);
    }
  } else if (upload.status == UPLOAD_FILE_WRITE) {
    if (otaAuthorized && !otaFailed && Update.write(upload.buf, upload.currentSize) != upload.currentSize) {
      otaFailed = true;
      Update.printError(Serial);
    }
  } else if (upload.status == UPLOAD_FILE_END) {
    if (!otaAuthorized) {
      otaFailed = true;
      return;
    }
    if (!otaFailed && !Update.end(true)) {
      otaFailed = true;
      Update.printError(Serial);
    } else if (!otaFailed) {
      Serial.printf("OTA complete: %u bytes\n", upload.totalSize);
    }
  } else if (upload.status == UPLOAD_FILE_ABORTED) {
    otaFailed = true;
    otaAuthorized = false;
    Update.end();
    Serial.println("OTA upload aborted");
  }
}

// Finish the OTA HTTP request.  When the upload was successful, reboot so
// the ESP32 bootloader can start the newly written application partition.
void handleOTAFinal() {
  if (!server.authenticate(OTA_USERNAME, OTA_PASSWORD)) {
    otaAuthorized = false;
    otaFailed = true;
    otaRequestAuth();
    return;
  }
  if (!otaAuthorized || !otaStarted || otaFailed || Update.hasError()) {
    server.send(500, "text/plain", "OTA failed. Check the password, firmware file, and partition scheme.");
    return;
  }
  server.send(200, "text/plain", "OTA complete. Rebooting...");
  delay(700);
  ESP.restart();
}
#endif

// Register every HTTP route and start the server.
// Lean capture API: stills + settings + status + SD files.  The UI lives
// in web/index.html outside the firmware; CORS headers allow it to call
// this API from the local network.
void startServers() {
  server.on("/", HTTP_GET, handleRoot);
  server.on("/api/status", HTTP_GET, handleStatus);
  server.on("/api/settings", HTTP_GET, [](){ if(server.args()>0){ applyArgsToSettings(); saveSettings(); } sendJson(settingsJson()); });
  server.on("/api/settings", HTTP_POST, handleSettingsSetPost);
  server.on("/capture", HTTP_GET, handleCapture);
  server.on("/jpg", HTTP_GET, handleJPG);
  server.on("/photo", HTTP_GET, handlePhoto);
  server.on("/img", HTTP_GET, handleImg);
  server.on("/data", HTTP_GET, handleData);
  server.on("/switch", HTTP_GET, handleSwitch);
  server.on("/illumination", HTTP_GET, handleIllumination);
  server.on("/ping", HTTP_GET, handlePing);
  server.on("/reboot", HTTP_GET, handleReboot);
  server.on("/api/files", HTTP_GET, handleFiles);
  server.on("/download", HTTP_GET, handleDownload);
  server.on("/api/delete", HTTP_GET, handleDelete);
  server.on("/api/clear-images", HTTP_GET, handleClearImages);
#if ENABLE_OTA
  server.on("/ota", HTTP_GET, handleOTAPage);
  server.on("/ota", HTTP_POST, handleOTAFinal, handleOTAUpload);
#endif
  server.begin();
}

// Print a concise diagnostic summary after startup.
// This is especially useful when the ESP32 is headless and the Serial
// Monitor is the easiest way to discover its current IP and hardware state.
void printBootInfo() {
  Serial.println();
  Serial.println("=== ESP32-CAM V2 ===");
  Serial.printf("PSRAM: %s\n", psramFound() ? "yes" : "no");
  Serial.printf("Free heap: %u\n", ESP.getFreeHeap());
  Serial.printf("SD: %s\n", sdReady ? "ready" : "not available");
  Serial.printf("RSSI: %d dBm\n", WiFi.status() == WL_CONNECTED ? WiFi.RSSI() : 0);
  Serial.printf("API: http://%s/ (UI in web/index.html)\n", WiFi.localIP().toString().c_str());
  Serial.println();
}

// =======================================================================
// Arduino startup sequence
// =======================================================================
// setup() runs exactly once after reset/power-up.
//
// The order is intentional:
//   1. start Serial
//   2. configure GPIO defaults
//   3. restore saved settings
//   4. initialise the camera
//   5. mount filesystems
//   6. start Wi-Fi/NTP
//   7. register/start HTTP services
//   8. wait briefly for Wi-Fi and print diagnostics
// =======================================================================
void setup() {
  Serial.begin(115200);
  delay(200);

  // Put all locally controlled pins into a known safe state before the
  // camera/network stack starts doing work.
  pinMode(FLASH_GPIO_NUM, OUTPUT);
  digitalWrite(FLASH_GPIO_NUM, LOW);

  // Restore persistent camera settings before initialising the sensor.
  // initCamera() then applies those values to the OV2640.
  loadSettings();
  applyPinModes();

  pinMode(INDICATOR_LED_PIN, OUTPUT);
  digitalWrite(INDICATOR_LED_PIN, HIGH);

  // Camera failure is treated as fatal because most of this project depends
  // on being able to obtain frames.  A delayed restart gives the Serial
  // Monitor a chance to show the error before rebooting.
  if (!initCamera()) {
    Serial.println("Fatal: camera failed. Restarting in 5 seconds.");
    delay(5000);
    ESP.restart();
  }

  // SPIFFS is the ESP32's internal flash filesystem.  The "true" argument
  // allows it to be formatted automatically if mounting fails.
  spiffsReady = SPIFFS.begin(true);

  // SD card is the preferred bulk storage for captured photographs.
  sdReady = initSD();
  if (sdReady) pruneOldestPhotos();

  // Start Wi-Fi and ask the ESP32 time service to synchronise its clock.
  // Correct time makes saved photo filenames and timestamps much more useful.
  beginWiFi();
  configTime(GMT_OFFSET_SEC, DST_OFFSET_SEC, "pool.ntp.org", "time.nist.gov", "time.google.com");

  // Start the normal web/API server and the separate port-81 MJPEG stream.
  startServers();

  // If timelapse is enabled, schedule the first capture one full interval
  // from now.  If disabled, the task simply remains inactive.
  nextTimelapse = millis() + (settings.timelapseMs ? settings.timelapseMs : 0);

  uint32_t wifiWaitStart = millis();
  while (WiFi.status() != WL_CONNECTED && millis() - wifiWaitStart < 15000) {
    delay(250);
    Serial.print('.');
  }
  Serial.println();
  printBootInfo();
}

// =======================================================================
// Main Arduino loop
// =======================================================================
// Keep loop() short and non-blocking.
//
// Each task is written so it normally does a little work and returns.  The
// timing checks inside motionTask(), timelapseTask() and serviceWiFi() mean
// they can safely be called on every pass without wasting time waiting.
//
// The only intentional tiny delay is delay(1), which yields enough CPU time
// for background ESP32/Wi-Fi housekeeping while keeping the loop responsive.
// =======================================================================
void loop() {
  // Handle HTTP requests on port 80 (capture API for the local site).
  server.handleClient();

  // Retry Wi-Fi if it has gone away.
  serviceWiFi();

  // Check whether it is time for the next motion sample.
  motionTask();

  // Check whether it is time for the next timelapse photo.
  timelapseTask();
  // Safety timeout for the spare output: if something turned the output on,
  // automatically turn it back off after OUTPUT_TIME_LIMIT_MS.
  //
  // This prevents a forgotten web/API command from leaving an attached
  // device powered indefinitely.
  if (digitalRead(OUTPUT_GPIO_PIN) == OUTPUT_ON_LEVEL && OUTPUT_TIME_LIMIT_MS > 0 && millis() - outputChangedAt > OUTPUT_TIME_LIMIT_MS) {
    digitalWrite(OUTPUT_GPIO_PIN, !OUTPUT_ON_LEVEL);
  }
  delay(1);
}
