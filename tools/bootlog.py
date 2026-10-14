#!/usr/bin/env python3
"""Capture the ESP32-CAM serial log safely (AGENTS.md hard rule).

Raw `stty`/`cat` on /dev/ttyUSB0 toggles the CH340's DTR/RTS lines, which
on this board are wired to EN/GPIO0 and drop the chip into download mode.
This script opens the port with both lines held deasserted, optionally
pulses RTS once (EN low → high with GPIO0 high = a normal reset into the
app), and prints what the firmware says for a fixed time.

    python3 tools/bootlog.py                 # reset, capture 60 s
    python3 tools/bootlog.py --no-reset -t 120
    python3 tools/bootlog.py --cmd status    # send a console command after boot

Caveat: Linux asserts DTR/RTS when the tty is opened, before pyserial can
hold them low, so even --no-reset reboots this board (boot counter +1,
warm-up discards, WiFi rejoin). Every capture costs a reboot — keep link
diagnosis on the server journal, not the serial port.
"""
import argparse
import sys
import time

import serial


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", default="/dev/ttyUSB0")
    ap.add_argument("-t", "--seconds", type=float, default=60)
    ap.add_argument("--no-reset", action="store_true", help="attach without resetting the board")
    ap.add_argument("--cmd", help="serial console command to send once (e.g. status, net)")
    ap.add_argument("--cmd-after", type=float, default=8, help="seconds after start to send --cmd")
    a = ap.parse_args()

    s = serial.Serial()
    s.port, s.baudrate, s.timeout = a.port, 115200, 0.2
    s.dtr = False  # GPIO0 high: never download mode
    s.rts = False  # EN released
    s.open()
    if not a.no_reset:
        s.rts = True   # EN low: hold in reset
        time.sleep(0.1)
        s.rts = False  # release: boots the app (dtr=False keeps GPIO0 high)
    start = time.time()
    sent = False
    try:
        while time.time() - start < a.seconds:
            if a.cmd and not sent and time.time() - start >= a.cmd_after:
                s.write((a.cmd + "\n").encode())
                sent = True
            line = s.readline()
            if line:
                sys.stdout.write(line.decode("utf-8", "replace"))
                sys.stdout.flush()
    finally:
        s.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
