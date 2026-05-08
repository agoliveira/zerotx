// i2c_lcd.h - I2C-attached HD44780 character LCD subsystem.
//
// Targets the LCM2002 (20x2 alphanumeric) on a PCF8574-based I2C
// backpack. Other geometries (16x2, 20x4) work too: SET geom changes
// kRows/kCols at runtime. The default is 20x2.
//
// I2C bus is the Mega's hardware Wire on pins 20/21. Bus pins are
// not in the HAL because Wire doesn't accept pin remapping on AVR.
//
// The I2C address is auto-detected at boot via the hd44780 library's
// scan helper. If two LCDs are present, only the first is used; for
// multi-LCD support, extend kInstanceCount and add per-instance
// addresses (deferred).
//
// Protocol:
//
//   SET lcd.0 line <row> <text...>     Write text to a row.
//                                      Multi-token text is space-joined.
//   SET lcd.0 clear                    Clear display.
//   SET lcd.0 backlight <0|1>          Backlight off/on.
//   SET lcd.0 cursor <off|on|blink>    Cursor visibility mode.
//   SET lcd.0 geom <cols> <rows>       Reconfigure for non-default size.
//   SET lcd.0 addr <0x..>              Force a specific I2C address;
//                                      re-initializes the LCD. Use
//                                      this to disambiguate when two
//                                      backpacks are on the same bus.
//   GET lcd.0                          Reports addr, geom, backlight.

#ifndef ZEROTX_IO_I2C_LCD_H
#define ZEROTX_IO_I2C_LCD_H

#include <Arduino.h>
#include <hd44780.h>
#include <hd44780ioClass/hd44780_I2Cexp.h>

#include "../subsystem.h"

namespace zerotx {

class I2cLcd : public Subsystem {
public:
  I2cLcd() {}

  const char* name() const override { return "lcd"; }
  uint8_t count() const override { return 1; }

  void begin(Stream& out) override;
  bool handle(uint8_t instance, const proto::Command& cmd, Stream& out) override;

private:
  // Default geometry: LCM2002 = 20 columns, 2 rows.
  uint8_t cols_ = 20;
  uint8_t rows_ = 2;

  // 0 = auto-detect at begin(). Operator can pin via SET addr.
  uint8_t i2c_addr_ = 0;
  bool    backlight_ = true;

  hd44780_I2Cexp* lcd_ = nullptr;
  alignas(hd44780_I2Cexp) uint8_t lcd_storage_[sizeof(hd44780_I2Cexp)];

  bool reinit(Stream& out);
  bool handleSet(const proto::Command& cmd, Stream& out);
  void handleGet(Stream& out);
};

}  // namespace zerotx

#endif  // ZEROTX_IO_I2C_LCD_H
