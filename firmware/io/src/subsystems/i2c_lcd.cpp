// i2c_lcd.cpp - I2C HD44780 LCD subsystem implementation.

#include "i2c_lcd.h"

#include <new>      // placement new
#include <string.h>
#include <stdlib.h>

namespace zerotx {

void I2cLcd::begin(Stream& out) {
  if (!reinit(out)) {
    proto::writeError(out, "lcd.0", "init-failed");
  } else {
    proto::writeEvent(out, "lcd.0", "ready");
  }
}

bool I2cLcd::reinit(Stream& out) {
  // Construct or reconstruct the driver. If we previously placed an
  // object here, this overwrites it; the hd44780 library doesn't
  // allocate any external resources that need destructor cleanup.
  if (i2c_addr_ != 0) {
    lcd_ = new (lcd_storage_) hd44780_I2Cexp(i2c_addr_);
  } else {
    // Auto-detect address by scanning the I2C bus.
    lcd_ = new (lcd_storage_) hd44780_I2Cexp();
  }

  int rc = lcd_->begin(cols_, rows_);
  if (rc != 0) {
    char buf[40];
    snprintf(buf, sizeof(buf), "init rc=%d", rc);
    proto::writeError(out, "lcd.0", buf);
    lcd_ = nullptr;   // mark as unusable
    return false;
  }

  if (backlight_) lcd_->backlight();
  else            lcd_->noBacklight();

  return true;
}

bool I2cLcd::handle(uint8_t instance, const proto::Command& cmd, Stream& out) {
  if (instance != 0) return false;
  if (!lcd_) {
    proto::writeError(out, "lcd.0", "not-initialized");
    return true;
  }

  const char* verb = cmd.verb();
  if (!verb) return false;
  if (strcmp(verb, "SET") == 0) return handleSet(cmd, out);
  if (strcmp(verb, "GET") == 0) { handleGet(out); return true; }
  return false;
}

bool I2cLcd::handleSet(const proto::Command& cmd, Stream& out) {
  const char* p = cmd.param();
  if (!p) {
    proto::writeError(out, "lcd.0", "missing-param");
    return true;
  }

  if (strcmp(p, "line") == 0) {
    const char* rowStr = cmd.arg(0);
    if (!rowStr) { proto::writeError(out, "lcd.0", "missing-row"); return true; }
    int row = atoi(rowStr);
    if (row < 0 || row >= rows_) {
      proto::writeError(out, "lcd.0", "invalid-row"); return true;
    }
    // Glue tokens 1..N back into a single string with single spaces.
    // Buffer caps at cols_+1; longer text gets truncated rather than
    // wrapping (we never want ambiguous overflow into the next row).
    char buf[33];   // covers up to 32 cols
    if (cols_ + 1 > sizeof(buf)) {
      proto::writeError(out, "lcd.0", "geometry-too-wide");
      return true;
    }
    size_t pos = 0;
    for (size_t i = 1; ; ++i) {
      const char* tok = cmd.arg(i);
      if (!tok) break;
      if (pos > 0 && pos < (size_t)cols_) buf[pos++] = ' ';
      while (*tok && pos < (size_t)cols_) buf[pos++] = *tok++;
    }
    // Pad the rest of the row with spaces so leftover characters from
    // a previous longer line are erased.
    while (pos < cols_) buf[pos++] = ' ';
    buf[pos] = '\0';

    lcd_->setCursor(0, (uint8_t)row);
    lcd_->print(buf);
    return true;
  }

  if (strcmp(p, "clear") == 0) {
    lcd_->clear();
    return true;
  }

  if (strcmp(p, "backlight") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "lcd.0", "missing-state"); return true; }
    backlight_ = (*v == '1');
    if (backlight_) lcd_->backlight();
    else            lcd_->noBacklight();
    return true;
  }

  if (strcmp(p, "cursor") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "lcd.0", "missing-mode"); return true; }
    if      (strcmp(v, "off")   == 0) { lcd_->noCursor(); lcd_->noBlink(); }
    else if (strcmp(v, "on")    == 0) { lcd_->cursor();   lcd_->noBlink(); }
    else if (strcmp(v, "blink") == 0) { lcd_->cursor();   lcd_->blink();   }
    else { proto::writeError(out, "lcd.0", "invalid-mode"); return true; }
    return true;
  }

  if (strcmp(p, "geom") == 0) {
    const char* c = cmd.arg(0);
    const char* r = cmd.arg(1);
    if (!c || !r) { proto::writeError(out, "lcd.0", "missing-geom"); return true; }
    int new_c = atoi(c);
    int new_r = atoi(r);
    if (new_c < 8 || new_c > 32 || new_r < 1 || new_r > 4) {
      proto::writeError(out, "lcd.0", "invalid-geom");
      return true;
    }
    cols_ = (uint8_t)new_c;
    rows_ = (uint8_t)new_r;
    if (!reinit(out)) {
      proto::writeError(out, "lcd.0", "reinit-failed");
    }
    return true;
  }

  if (strcmp(p, "addr") == 0) {
    const char* v = cmd.arg(0);
    if (!v) { proto::writeError(out, "lcd.0", "missing-addr"); return true; }
    // Accept hex (0x..) or decimal.
    long n = strtol(v, nullptr, 0);
    if (n < 0x08 || n > 0x77) {
      proto::writeError(out, "lcd.0", "invalid-addr");
      return true;
    }
    i2c_addr_ = (uint8_t)n;
    if (!reinit(out)) {
      proto::writeError(out, "lcd.0", "reinit-failed");
    }
    return true;
  }

  proto::writeError(out, "lcd.0", "unknown-param");
  return true;
}

void I2cLcd::handleGet(Stream& out) {
  char body[80];
  if (i2c_addr_ == 0) {
    snprintf(body, sizeof(body),
        "lcd.0 addr=auto cols=%u rows=%u backlight=%d",
        (unsigned)cols_, (unsigned)rows_,
        backlight_ ? 1 : 0);
  } else {
    snprintf(body, sizeof(body),
        "lcd.0 addr=0x%02X cols=%u rows=%u backlight=%d",
        (unsigned)i2c_addr_, (unsigned)cols_, (unsigned)rows_,
        backlight_ ? 1 : 0);
  }
  proto::writeResponse(out, body);
}

}  // namespace zerotx
