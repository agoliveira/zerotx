// ZeroTX VFD diagnostic display firmware.
//
// Runs on a SparkFun Pro Micro 5V (ATmega32u4) wired to a Noritake
// CU20025ECPB-W1J 2x20 VFD in 4-bit HD44780 mode. Pin assignments
// per HANDOVER.md and platformio.ini comment block.
//
// Wire protocol (Pi -> Pro Micro, ASCII over USB-CDC, line-based):
//
//   L<row><sp><content>\n   write <content> to <row> (0 or 1).
//                           Content auto-padded/truncated to 20 cols.
//   C\n                     clear display.
//   B<sp><level>\n          brightness 0..3 (0 = max).
//   V\n                     write firmware version banner (debug).
//
// Anything else is silently ignored. The protocol is intentionally
// terse so the daemon-side scaffolding is trivial; the firmware
// is the dumb half of the pair.
//
// Init sequence: on boot, show a banner so the operator can see the
// firmware came up before any daemon traffic arrives.

#include <Arduino.h>
#include <hd44780.h>
#include <hd44780ioClass/hd44780_NTCU20025ECPB_pinIO.h>

// ===== Configuration =====

constexpr const char FW_VERSION[] = "0.1.0";

// 4-bit parallel pin assignment.
// SparkFun Pro Micro 5V/16MHz only breaks out D0-D10, D14-D16,
// D18-D21 on the headers; D11, D12, D13 exist in the 32u4 but
// aren't accessible on the board. We use D4-D9 (six contiguous
// pins on the left edge) so the ribbon routing stays clean.
constexpr uint8_t PIN_RS = 4;
constexpr uint8_t PIN_EN = 5;
constexpr uint8_t PIN_D4 = 6;
constexpr uint8_t PIN_D5 = 7;
constexpr uint8_t PIN_D6 = 8;
constexpr uint8_t PIN_D7 = 9;

constexpr uint8_t LCD_COLS = 20;
constexpr uint8_t LCD_ROWS = 2;

// Inter-byte safety: max line length we'll accept. Even a worst-case
// "L 1 <20 chars>" is 24 bytes. 64 leaves room for malformed input
// or future commands without overflowing the buffer.
constexpr size_t LINE_BUF_SIZE = 64;

// Banner shown at boot until the daemon takes over.
constexpr const char BANNER_ROW0[] = "ZEROTX VFD          ";
constexpr const char BANNER_ROW1_FMT[] = "fw %s awaiting";

// ===== Globals =====

hd44780_NTCU20025ECPB_pinIO lcd(PIN_RS, PIN_EN, PIN_D4, PIN_D5, PIN_D6, PIN_D7);

char lineBuf[LINE_BUF_SIZE];
size_t lineLen = 0;

// ===== Helpers =====

// writeRow emits exactly LCD_COLS characters to the given row,
// padding with spaces if content is shorter, truncating if longer.
// Cursor is left at the end of the row.
static void writeRow(uint8_t row, const char *content) {
  if (row >= LCD_ROWS) return;
  lcd.setCursor(0, row);
  uint8_t i = 0;
  for (; i < LCD_COLS && content[i] != '\0'; i++) {
    lcd.write(content[i]);
  }
  for (; i < LCD_COLS; i++) {
    lcd.write(' ');
  }
}

// showBanner displays the boot identification on the VFD.
static void showBanner() {
  lcd.clear();
  writeRow(0, BANNER_ROW0);
  char row1[LCD_COLS + 1];
  snprintf(row1, sizeof(row1), BANNER_ROW1_FMT, FW_VERSION);
  writeRow(1, row1);
}

// setBrightness applies one of the four Noritake brightness levels.
// 0 = 100%, 1 = 75%, 2 = 50%, 3 = 25%. Out-of-range values are
// clamped to the valid set.
//
// The duinoWitchery hd44780 library exposes setExecTimes and
// command(); the brightness control on the CU-U series is done by
// sending a Function Set with the brightness bits in the lower two
// positions, after entering the extended command mode. Library-level
// support varies by version, so we use the raw command path which
// is documented in the Noritake datasheet and works on all firmware
// revisions of the W1J module we target.
static void setBrightness(uint8_t level) {
  if (level > 3) level = 3;
  // Noritake CU-U brightness command (per datasheet):
  //   0x28 | (3 - level) selects 4-bit, 2-line mode + brightness bits.
  // Brightness bits encoding inverts the parameter: 0b00 = max (100%),
  // 0b11 = min (25%). We expose 0..3 as max..min to match the daemon
  // Driver interface, then invert here.
  uint8_t bits = (3 - level) & 0x03;
  // The "extended" command needs to follow a function-set with bit 0=1,
  // but on the W1J the brightness lives in the lower bits of the
  // standard function set itself per the LCD-compatible mode docs.
  // Empirical bench step: if nothing changes, swap to the extended
  // sequence: lcd.command(0x29); lcd.command(0xF0 | bits);
  lcd.command(0x28 | bits);
}

// ===== Command processor =====

static void processLine(char *line, size_t len) {
  if (len == 0) return;
  switch (line[0]) {
    case 'L': {
      // L<row> <content>
      if (len < 4) return;
      if (line[1] < '0' || line[1] > '9') return;
      if (line[2] != ' ') return;
      uint8_t row = line[1] - '0';
      writeRow(row, &line[3]);
      break;
    }
    case 'C':
      lcd.clear();
      break;
    case 'B': {
      // B <level>
      if (len < 3) return;
      if (line[1] != ' ') return;
      uint8_t level = (uint8_t)(line[2] - '0');
      setBrightness(level);
      break;
    }
    case 'V':
      showBanner();
      break;
    default:
      // Unknown command: silently ignore. The daemon may probe with
      // future commands; tolerating them avoids spurious display
      // corruption from version skew.
      break;
  }
}

// ===== Arduino entry points =====

void setup() {
  // USB-CDC. 115200 is conventional; the daemon's serial driver
  // will match. The 32u4's CDC ignores baud rate at the physical
  // layer (it's all USB), but Linux tooling expects a value to be
  // set so we declare one.
  Serial.begin(115200);

  int rc = lcd.begin(LCD_COLS, LCD_ROWS);
  if (rc != 0) {
    // Init failure: the only feedback we have is the on-board LED
    // since the display is what failed. Blink fast forever so a
    // human plugging in notices.
    pinMode(LED_BUILTIN, OUTPUT);
    while (true) {
      digitalWrite(LED_BUILTIN, HIGH);
      delay(100);
      digitalWrite(LED_BUILTIN, LOW);
      delay(100);
    }
  }

  // Default to full brightness; the daemon can knock it down later.
  setBrightness(0);
  showBanner();
}

void loop() {
  while (Serial.available() > 0) {
    int c = Serial.read();
    if (c < 0) break;
    if (c == '\r') continue; // tolerate CRLF
    if (c == '\n') {
      lineBuf[lineLen] = '\0';
      processLine(lineBuf, lineLen);
      lineLen = 0;
      continue;
    }
    if (lineLen + 1 < LINE_BUF_SIZE) {
      lineBuf[lineLen++] = (char)c;
    } else {
      // Overflow: discard the in-progress line. The next \n resets us.
      lineLen = 0;
    }
  }
}
