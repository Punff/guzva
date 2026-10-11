// guzva — per-device settings: TEMPLATE.
//
// Copy this file to firmware/include/settings.h and fill in the two secrets
// (WIFI_PASSWORD, DEVICE_TOKEN). settings.h is gitignored and must stay out
// of the repo: it is the only file with device credentials in it.
//
//   cp firmware/include/settings.example.h firmware/include/settings.h
//
// The bench CA below is public material (the private key lives only on the
// VPS), so it ships here — a fresh clone can build and reach the bench
// server after filling in the secrets. Rotate the token with:
//   guzva provision -slug <slug> -token <new>   (on the VPS)
// …or mint a fresh one and re-flash.

// ---------------------------------------------------------------- WiFi -----
static const char *WIFI_SSID     = "***REMOVED***";
static const char *WIFI_PASSWORD = "CHANGE-ME";

// mDNS name is "<MDNS_HOST_PREFIX>-<serial from efuse MAC>":
// guzva-<serial>.local (PLAN §5 LAN cage).
static const char *MDNS_HOST_PREFIX = "guzva";

// ---------------------------------------------- Ingest server (bench) --------
// Hostname only — no scheme, no path. The firmware always POSTs to
// https://<INGEST_HOST>:<INGEST_PORT>/v1/ingest (proto-v1).
// Plain IP today (bench VPS, self-signed cert below); ingest.guzva.ba flips
// in here + NVS later — no other change.
static const char *INGEST_HOST = "34.139.58.143";   // guzva (GCP e2-micro)
static const uint16_t INGEST_PORT = 443;

// CA root PEM used with WiFiClientSecure::setCACert() — the server cert must
// chain to it; verification is always on. Never set INSECURE_TLS=1 in a
// production build (grep-gated, see platformio.ini build_flags).
//
// Bench cert: self-signed, epoch-valid 1970→2099 (the ESP32 has no RTC and
// no NTP on the capture path, so the handshake runs with a 1970 clock). SAN
// covers the VPS IP as IP: *and* DNS: (mbedtls 2.26 in the arduino core
// ignores iPAddress SANs — it matches the DNS entry). Regenerate on the VPS
// (see tools/bench-cert.sh) → paste the new PEM here → re-flash.
static const char CA_ROOT_PEM[] = R"CERT(
-----BEGIN CERTIFICATE-----
MIIDjzCCAnegAwIBAgIUbx1+yV8ugKE1OwFg7lcFvtxnjsYwDQYJKoZIhvcNAQEL
BQAwMDEWMBQGA1UECgwNY2FtdGVzdC1iZW5jaDEWMBQGA1UEAwwNMzQuMTM5LjU4
LjE0MzAgFw03MDAxMDEwMDAwMDBaGA8yMDk5MTIzMTIzNTk1OVowMDEWMBQGA1UE
CgwNY2FtdGVzdC1iZW5jaDEWMBQGA1UEAwwNMzQuMTM5LjU4LjE0MzCCASIwDQYJ
KoZIhvcNAQEBBQADggEPADCCAQoCggEBAIweXhxrJGKXbP5CB8rYtZRFZriNiTYA
K0KHYbwrlA2R7bpZHhOoedUso+WfB3UkOGxcHuGsYh4rS91lJPkG58sahzxEzLnS
O0qJD4Pnj+oU9r/bEEohAU8fqiTi5QyeWHz2DH4Hx/qiYbvtPO3LkFbsTrzNbBex
uJkVsF8mBV1js3Dix9pEUPilMeOMklXuWAIe8gobyFGCeZGEdPRGVSUtSZIiuI4H
fTH3l3hvz+2fBscIF7Tukv2P6mX0bB8+2JtOCAzBR+0dBwxk8kqF4yoPPsaLX1Js
ZFguZT+Uv7kyNG78Syy4frMti+I2uQhZyD38M7S3tqZTW7JJyxCgrMkCAwEAAaOB
njCBmzAdBgNVHQ4EFgQUnZ8AZ9k+ocvb2064N+zU47hbM5IwHwYDVR0jBBgwFoAU
nZ8AZ9k+ocvb2064N+zU47hbM5IwOAYDVR0RBDEwL4cEIos6j4cEfwAAAYINMzQu
MTM5LjU4LjE0M4IFZ3V6dmGCC2d1enZhLmxvY2FsMA8GA1UdEwEB/wQFMAMBAf8w
DgYDVR0PAQH/BAQDAgKkMA0GCSqGSIb3DQEBCwUAA4IBAQBJCzL5hEOwvR4X0Zza
3YwBW+wtaNBxPr89bHguEA6wJiIpow8x6wiw7ApFh99FhPWTE2czgQBsuS1GwWIp
3MGc2y2MigQFv+EGr40DdCHTWjYVdqBJ+zBfH/TY8oOdOSuAqq91wwkzt8+j33y9
LJTSL8/C30ACHkworPdLGvRdVLylmeHv/tBjXpbcN42ynRiiAnnDucsrNiM8Dl45
gP3zxvK/72K32wQITyWjLXRp4UFc5QS+lZGKJD8HtSrmEbekUvXiaq+/kWKez9ew
AAdQL7LxOs+TRRhA0Xtj4S+M3DNnnjbczg3jk/9WViwAsxP0bqiDM8yeda5zgzUY
gl25
-----END CERTIFICATE-----
)CERT";

// ------------------------------------------------------ Device identity -----
// Reachability probe target for the serial "net" command: the installer's PC
// on the bench network (LAN leg of the probe, port 8080).
static const char *LAN_PROBE_HOST = "192.168.1.11";

// Bearer token for `Authorization: Bearer <device token>` (proto-v1).
// Runtime order of precedence:
//   1. NVS namespace "prov", key "token"  (installer can re-provision)
//   2. this compile-time fallback
static const char *DEVICE_TOKEN = "CHANGE-ME";
