// ZeroTX HUB75 display firmware
//
// Target: ESP32 classic (any variant with enough GPIO for HUB75)
// Hardware: two chained P2.5 64x32 panels = 128x32 logical surface
// Library: ESP32-HUB75-MatrixPanel-DMA (mrfaptastic)
//   https://github.com/mrfaptastic/ESP32-HUB75-MatrixPanel-DMA
//
// Build environment: PlatformIO recommended (see platformio.ini in
// this directory). Arduino IDE works too if you install the library
// via the Library Manager.
//
// Protocol: see docs/protocols/display.md
//
// Wiring (default library pinout, change in setup() if your board
// differs):
//   R1=25  G1=26  B1=27
//   R2=14  G2=12  B2=13
//   A=23   B=19   C=5   D=17   E=22 (E is for 1/32 scan; not needed here)
//   LAT=4  OE=15  CLK=16
//
// Power: panels are 5V, can pull 6A peak combined. Use a separate 5V
// rail; do NOT power from the ESP32's USB rail.
//
// Notes on conservatism:
// - All rendering uses the library's built-in font and direct draw
//   primitives. No custom fonts, no double-buffering tricks
// - Mode renderers are structured as full-screen redraws on transition
//   plus minimal updates per state change. Performance is not yet
//   optimized; the panels can refresh at hundreds of Hz so we don't
//   need to be clever
// - Serial parser is a simple line accumulator; no fancy state machine
// - Heartbeat is a fixed 5s interval

#include <Arduino.h>
#include <ESP32-HUB75-MatrixPanel-I2S-DMA.h>
#include <U8g2_for_Adafruit_GFX.h>

// Custom hand-designed pixel font for the big values - 15 tall x
// 10 wide per glyph, 2-pixel-wide strokes, upright industrial
// aesthetic. Designed specifically for this LED matrix at this
// tile size; no existing font we tried fit the panel well.
//
// Bitmap format: each glyph is 15 rows of uint16_t. Each row uses
// the lower 10 bits (bit 9 = leftmost pixel, bit 0 = rightmost).
// Top 6 bits unused.
//
// Glyph width = 10, advance = 11 (10 + 1px gap between glyphs).
constexpr int CUSTOM_GLYPH_WIDTH = 12;
constexpr int CUSTOM_GLYPH_HEIGHT = 25;
constexpr int CUSTOM_GLYPH_ADVANCE = 13;

// Helper macro: 12-bit row from binary literal-ish mask.
#define R(b11,b10,b9,b8,b7,b6,b5,b4,b3,b2,b1,b0) \
  (uint16_t)((b11)<<11 | (b10)<<10 | (b9)<<9 | (b8)<<8 | (b7)<<7 | (b6)<<6 | (b5)<<5 | (b4)<<4 | (b3)<<3 | (b2)<<2 | (b1)<<1 | (b0))

// Glyph table indexed by ASCII char minus a base. We support
// '0'-'9' (0x30-0x39) and '.' (0x2E). Stored as 12 entries (0..11)
// where 0..9 = digits, 10 = '.', 11 = empty (space/unknown).
static const uint16_t CUSTOM_GLYPHS[12][CUSTOM_GLYPH_HEIGHT] = {
  // ===== '0' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,1,1,0,0,0,0,0,0,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '1' (7-seg style) =====
  {
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
  },
  // ===== '2' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '3' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '4' (7-seg style) =====
  {
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,1,1,0,0,0,0,0,0,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
  },
  // ===== '5' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '6' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(1,1,1,0,0,0,0,0,0,0,0,0),
    R(0,1,1,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '7' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
  },
  // ===== '8' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '9' (7-seg style) =====
  {
    R(0,0,0,1,1,1,1,1,1,0,0,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(1,1,1,0,0,0,0,0,0,1,1,1),
    R(0,1,1,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,0,0,0,0,0,0,1,1,1),
    R(0,0,0,1,1,1,1,1,1,1,1,0),
    R(0,0,1,1,1,1,1,1,1,1,0,0),
    R(0,0,0,1,1,1,1,1,1,0,0,0),
  },
  // ===== '.' (7-seg style) =====
  {
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,1,1,1,0,0,0,0,0),
    R(0,0,0,0,1,1,1,0,0,0,0,0),
    R(0,0,0,0,1,1,1,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
  },
  // ===== empty/space (index 11) =====
  {
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
    R(0,0,0,0,0,0,0,0,0,0,0,0),
  },
};
#undef R

// Map an ASCII char to glyph index. Returns 11 (empty) for chars
// not in the supported set.
static inline int custom_glyph_index(char c) {
  if (c >= '0' && c <= '9') return c - '0';
  if (c == '.') return 10;
  return 11;
}

// Font shortcuts. Custom font (above) handles big numeric values.
// U8g2 fonts cover everything else (mode names, alarm text, etc).
#define FONT_TEXT    u8g2_font_VCR_OSD_tr     // VCR-OSD for letters in mode names
#define FONT_SMALL   u8g2_font_5x7_tr         // tiny fallback for cramped places
#define FONT_DECIMAL u8g2_font_helvB08_tr     // small font for fractional digits + suffix

// ===== Configuration =====

constexpr int PANEL_WIDTH = 64;
constexpr int PANEL_HEIGHT = 32;
constexpr int PANELS_NUM = 2;

constexpr int LOGICAL_WIDTH = PANEL_WIDTH * PANELS_NUM;  // 128
constexpr int LOGICAL_HEIGHT = PANEL_HEIGHT;             // 32

constexpr unsigned long HEARTBEAT_INTERVAL_MS = 5000;
constexpr unsigned long IDLE_REDRAW_INTERVAL_MS = 1000;  // clock tick

constexpr const char* FW_VERSION = "0.19.0";

// ===== VFD/LCD palette =====
//
// Vintage avionics aesthetic: bright cyan-green for healthy data,
// dim cyan for labels, amber for caution, red for critical, dim
// grey for structural elements. All values RGB565.
//
// NOTE: These Waveshare panels have green and blue swapped relative
// to the standard HUB75 pinout. The green/blue pin remap in setup()
// fixes the wire-level swap so these RGB565 values mean what they
// say.

#define COLOR_BG          0x0000  // black
#define COLOR_VALUE       0x07F4  // bright cyan-green (VFD glow)
#define COLOR_LABEL       0x0309  // dimmer cyan-green
#define COLOR_CAUTION     0xFD20  // amber
#define COLOR_CRITICAL    0xF800  // red
#define COLOR_OK          0x07E0  // bright green
#define COLOR_SEPARATOR   0x2104  // dim grey for tile boundaries
#define COLOR_GAUGE_BG    0x18C3  // very dim grey for gauge background

// ===== Library setup =====

MatrixPanel_I2S_DMA *dma_display = nullptr;
U8G2_FOR_ADAFRUIT_GFX u8g2;

// ===== State =====

enum class Mode {
  IDLE,
  PREFLIGHT,
  FLIGHT,
  ALARM,
  RTH,
  POSTFLIGHT
};

struct State {
  // Flight state
  bool   armed       = false;
  bool   armed_known = false;
  float  bat_v       = 0.0f;
  bool   bat_known   = false;
  int    bat_pct     = 0;
  bool   batpct_known = false;
  int    alt_m       = 0;
  bool   alt_known   = false;
  int    dist_m      = 0;
  bool   dist_known  = false;
  int    spd_kmh     = 0;
  bool   spd_known   = false;
  int    link_pct    = 0;
  bool   link_known  = false;
  int    sats        = 0;
  bool   sats_known  = false;
  String flight_mode = "";
  String gps_fix     = "";
  int    time_s      = 0;
  bool   time_known  = false;

  // Alarm overlay
  bool   alarm_active = false;
  String alarm_level  = "";
  String alarm_text   = "";

  // One-shot message
  String msg_text     = "";
  unsigned long msg_started_ms = 0;

  // Brightness
  uint8_t brightness = 80; // 0-255 in library; we accept 0-100 from protocol

  // Trend tracking for animated bars. Updated when new state arrives;
  // used by render to draw moving chevrons. The "_at_ms" is the
  // timestamp of the last update so we can detect stale data and
  // freeze animation when the daemon hasn't sent anything recently.
  int    alt_m_prev      = 0;
  int    alt_delta       = 0;       // current - previous (signed)
  unsigned long alt_updated_ms = 0;
  int    dist_m_prev     = 0;
  int    dist_delta      = 0;
  unsigned long dist_updated_ms = 0;

  // Alarm thresholds pushed by the daemon at model load (see
  // DISP THRESHOLDS in the protocol doc). Per-domain "_set" flags
  // gate whether thresholds are honored: rendering falls back to
  // neutral colors for any domain whose flag is false.
  bool  bat_thresholds_set = false;
  float bat_warn_v  = 0.0f;
  float bat_crit_v  = 0.0f;
  float bat_min_v   = 0.0f;
  float bat_full_v  = 0.0f;

  bool bat_pct_warn_known = false;  // derived from bat_warn_v/bat_full_v
  int  bat_pct_warn = 0;            // ditto, in 0..100
  int  bat_pct_crit = 0;            // ditto

  bool alt_thresholds_set = false;
  int  alt_warn_m = 0;
  int  alt_crit_m = 0;

  bool dist_thresholds_set = false;
  int  dist_warn_m = 0;
  int  dist_crit_m = 0;

  bool link_thresholds_set = false;
  int  rssi_warn_dbm = 0;
  int  rssi_crit_dbm = 0;
  int  lq_warn_pct = 0;
  int  lq_crit_pct = 0;

  bool time_thresholds_set = false;
  int  time_warn_s = 0;
  int  time_crit_s = 0;
};

Mode current_mode = Mode::IDLE;
State state;

// Animation frame counter. Incremented every ANIM_FRAME_MS in the
// main loop and used to compute chevron position. Wraps freely.
unsigned long anim_frame = 0;
unsigned long last_anim_tick_ms = 0;
constexpr unsigned long ANIM_FRAME_MS = 100;  // 10fps
constexpr unsigned long TREND_STALE_MS = 2000; // freeze anim after this

// Serial input buffer.
constexpr size_t LINE_BUF_SIZE = 256;
char line_buf[LINE_BUF_SIZE];
size_t line_len = 0;

// Heartbeat timing.
unsigned long last_heartbeat_ms = 0;
unsigned long boot_ms = 0;

// Render dirty flag: set on state changes, cleared after redraw.
bool needs_redraw = true;
unsigned long last_redraw_ms = 0;

// ===== Forward decls =====

void handle_line(const char* line);
void send_ready();
void send_heartbeat();
void send_pong();
void send_error(const char* msg);
void render();
void render_idle();
void render_preflight();
void render_flight();
void render_alarm();
void render_rth();
void render_postflight();
void show_boot_banner();

// ===== Setup =====

void setup() {
  Serial.begin(115200);
  // Don't wait for serial; the daemon may connect later.

  // Panel configuration. The Waveshare P2.5 64x32 (and most 64x32
  // panels) are 1/16 scan, which means they only use 4 address lines
  // (A, B, C, D). The E pin is for 1/32 scan panels (typical of
  // 64x64) and isn't present on these panels at all.
  //
  // We explicitly mark E as unused so the library doesn't try to
  // drive a phantom GPIO. -1 (or HUB75_I2S_CFG::DEFAULT_HOST_ID
  // sentinel) tells the library to skip the pin.
  HUB75_I2S_CFG mxconfig(
    PANEL_WIDTH,    // module width
    PANEL_HEIGHT,   // module height
    PANELS_NUM      // chain length
  );
  mxconfig.gpio.e = -1;  // No E pin on 1/16 scan panels (Waveshare P2.5 64x32)

  // Waveshare P2.5 64x32 panels have GREEN and BLUE channels swapped
  // relative to the standard HUB75 pinout (documented Adafruit caveat
  // for 2.5mm pitch panels). Remap G1<->B1 and G2<->B2 so the
  // RGB565 color values in the firmware mean what they say.
  mxconfig.gpio.r1 = 25; mxconfig.gpio.g1 = 27; mxconfig.gpio.b1 = 26;
  mxconfig.gpio.r2 = 14; mxconfig.gpio.g2 = 13; mxconfig.gpio.b2 = 12;

  // Other pin overrides (uncomment if your wiring differs from defaults):
  // mxconfig.gpio.a = 23;  mxconfig.gpio.b = 19;  mxconfig.gpio.c = 5;
  // mxconfig.gpio.d = 17;  mxconfig.gpio.lat = 4; mxconfig.gpio.oe = 15;
  // mxconfig.gpio.clk = 16;

  dma_display = new MatrixPanel_I2S_DMA(mxconfig);
  if (!dma_display->begin()) {
    // Init failed. Without panels we can still respond to serial,
    // so don't halt; just log.
    Serial.println(F("DISP ERROR \"panel init failed\""));
  }
  dma_display->setBrightness8(state.brightness * 255 / 100);
  dma_display->clearScreen();

  // Initialize U8g2_for_Adafruit_GFX as the text rendering layer.
  // Direct setFont() on the HUB75 library has known positioning
  // bugs with custom fonts; the U8g2 layer renders cleanly and
  // gives us access to a large font catalog without any conversion
  // step.
  u8g2.begin(*dma_display);
  u8g2.setFontMode(1);              // transparent (background not erased)
  u8g2.setFontDirection(0);          // left-to-right
  // Identification banner. Brief visual confirmation that firmware
  // and panel are alive at the version we expect, before the
  // daemon takes over. The earlier stages of self-test (RGB
  // surfaces, color bars, A/B half-labels, seam marker) lived
  // here for bring-up; with the panel and chain order locked in,
  // they're no longer earning their boot-time cost.
  show_boot_banner();

  boot_ms = millis();
  send_ready();
  needs_redraw = true;
}

// show_boot_banner displays "ZEROTX <version>" briefly at boot so
// the operator can see firmware loaded and panel power is up before
// the daemon link is established. Held for ~800ms; the daemon's
// first frame will overwrite it.
void show_boot_banner() {
  if (!dma_display) return;
  dma_display->clearScreen();
  u8g2.setFont(FONT_TEXT);
  u8g2.setForegroundColor(dma_display->color565(0, 200, 160));
  int w_zerotx = u8g2.getUTF8Width("ZEROTX");
  u8g2.setCursor((LOGICAL_WIDTH - w_zerotx) / 2, 18);
  u8g2.print("ZEROTX");
  u8g2.setFont(FONT_SMALL);
  u8g2.setForegroundColor(dma_display->color565(0, 100, 80));
  int w_ver = u8g2.getUTF8Width(FW_VERSION);
  u8g2.setCursor((LOGICAL_WIDTH - w_ver) / 2, 30);
  u8g2.print(FW_VERSION);
  delay(800);
  dma_display->clearScreen();
}

// ===== Main loop =====

void loop() {
  // Drain serial input one byte at a time. Lines are terminated by
  // \n; \r is tolerated and stripped.
  while (Serial.available()) {
    int c = Serial.read();
    if (c < 0) break;
    if (c == '\r') continue;
    if (c == '\n') {
      line_buf[line_len] = '\0';
      handle_line(line_buf);
      line_len = 0;
      continue;
    }
    if (line_len + 1 < LINE_BUF_SIZE) {
      line_buf[line_len++] = (char)c;
    } else {
      // Overflow: drop the line, reset, log.
      line_len = 0;
      send_error("line too long");
    }
  }

  // Heartbeat.
  unsigned long now = millis();
  if (now - last_heartbeat_ms >= HEARTBEAT_INTERVAL_MS) {
    last_heartbeat_ms = now;
    send_heartbeat();
  }

  // Idle clock redraw.
  if (current_mode == Mode::IDLE && now - last_redraw_ms >= IDLE_REDRAW_INTERVAL_MS) {
    needs_redraw = true;
  }

  // Animation tick. While in flight mode, force redraw at 10fps so
  // the animated trend chevrons keep moving even when state is
  // static. Other modes don't need animation.
  if (current_mode == Mode::FLIGHT && now - last_anim_tick_ms >= ANIM_FRAME_MS) {
    last_anim_tick_ms = now;
    anim_frame++;
    needs_redraw = true;
  }

  // Render if dirty.
  if (needs_redraw) {
    render();
    needs_redraw = false;
    last_redraw_ms = now;
  }
}

// ===== Serial input handling =====

// Tokenize a line in-place, respecting double-quoted strings. Returns
// the number of tokens. Tokens point into the buffer (which is
// modified to insert null terminators).
//
// Quoted strings have their quotes stripped.
int tokenize(char* line, char* out_tokens[], int max_tokens) {
  int count = 0;
  char* p = line;
  while (*p && count < max_tokens) {
    while (*p == ' ' || *p == '\t') p++;
    if (!*p) break;
    if (*p == '"') {
      p++;
      char* start = p;
      while (*p && *p != '"') p++;
      if (*p == '"') {
        *p = '\0';
        p++;
      }
      out_tokens[count++] = start;
    } else {
      char* start = p;
      while (*p && *p != ' ' && *p != '\t') p++;
      if (*p) {
        *p = '\0';
        p++;
      }
      out_tokens[count++] = start;
    }
  }
  return count;
}

// Find a key=value arg. Returns the value string or nullptr.
const char* arg_value(char* tokens[], int count, int start, const char* key) {
  size_t key_len = strlen(key);
  for (int i = start; i < count; i++) {
    if (strncmp(tokens[i], key, key_len) == 0 && tokens[i][key_len] == '=') {
      return tokens[i] + key_len + 1;
    }
  }
  return nullptr;
}

// Set the current mode. No-op if already in that mode.
void set_mode(Mode m) {
  if (current_mode == m) return;
  current_mode = m;
  needs_redraw = true;
}

void handle_line(const char* line) {
  // Make a writable copy because tokenize mutates.
  static char buf[LINE_BUF_SIZE];
  strncpy(buf, line, sizeof(buf));
  buf[sizeof(buf) - 1] = '\0';

  char* tokens[24];
  int n = tokenize(buf, tokens, 24);
  if (n < 2) return;
  if (strcmp(tokens[0], "DISP") != 0) return;

  const char* cmd = tokens[1];

  if (strcmp(cmd, "MODE") == 0) {
    if (n < 3) return;
    if (strcmp(tokens[2], "IDLE") == 0) set_mode(Mode::IDLE);
    else if (strcmp(tokens[2], "PREFLIGHT") == 0) set_mode(Mode::PREFLIGHT);
    else if (strcmp(tokens[2], "FLIGHT") == 0) set_mode(Mode::FLIGHT);
    else if (strcmp(tokens[2], "ALARM") == 0) set_mode(Mode::ALARM);
    else if (strcmp(tokens[2], "RTH") == 0) set_mode(Mode::RTH);
    else if (strcmp(tokens[2], "POSTFLIGHT") == 0) set_mode(Mode::POSTFLIGHT);
    else send_error("unknown mode");
  }
  else if (strcmp(cmd, "STATE") == 0) {
    const char* v;
    if ((v = arg_value(tokens, n, 2, "armed")) != nullptr) {
      state.armed = (v[0] == '1');
      state.armed_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "bat")) != nullptr) {
      state.bat_v = atof(v);
      state.bat_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "batpct")) != nullptr) {
      state.bat_pct = atoi(v);
      state.batpct_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "alt")) != nullptr) {
      int new_alt = atoi(v);
      if (state.alt_known && new_alt != state.alt_m) {
        // Real change - shift current to prev, compute delta
        state.alt_m_prev = state.alt_m;
        state.alt_delta = new_alt - state.alt_m;
        state.alt_m = new_alt;
        state.alt_updated_ms = millis();
      } else if (!state.alt_known) {
        // First sample - no trend yet
        state.alt_m = new_alt;
        state.alt_m_prev = new_alt;
        state.alt_delta = 0;
        state.alt_known = true;
        state.alt_updated_ms = millis();
      }
      // else: same value resent, do nothing (preserve existing delta)
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "dist")) != nullptr) {
      int new_dist = atoi(v);
      if (state.dist_known && new_dist != state.dist_m) {
        state.dist_m_prev = state.dist_m;
        state.dist_delta = new_dist - state.dist_m;
        state.dist_m = new_dist;
        state.dist_updated_ms = millis();
      } else if (!state.dist_known) {
        state.dist_m = new_dist;
        state.dist_m_prev = new_dist;
        state.dist_delta = 0;
        state.dist_known = true;
        state.dist_updated_ms = millis();
      }
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "spd")) != nullptr) {
      state.spd_kmh = atoi(v);
      state.spd_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "link")) != nullptr) {
      state.link_pct = atoi(v);
      state.link_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "sats")) != nullptr) {
      state.sats = atoi(v);
      state.sats_known = true;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "mode")) != nullptr) {
      state.flight_mode = v;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "gps")) != nullptr) {
      state.gps_fix = v;
      needs_redraw = true;
    }
    if ((v = arg_value(tokens, n, 2, "time")) != nullptr) {
      state.time_s = atoi(v);
      state.time_known = true;
      needs_redraw = true;
    }
  }
  else if (strcmp(cmd, "ALARM") == 0) {
    if (n < 4) return;
    state.alarm_active = true;
    state.alarm_level = tokens[2];
    state.alarm_text = tokens[3];
    set_mode(Mode::ALARM);
  }
  else if (strcmp(cmd, "CLEAR-ALARM") == 0) {
    state.alarm_active = false;
    state.alarm_level = "";
    state.alarm_text = "";
    needs_redraw = true;
    // Mode does not auto-revert; daemon issues new MODE if needed.
  }
  else if (strcmp(cmd, "MSG") == 0) {
    if (n < 3) return;
    state.msg_text = tokens[2];
    state.msg_started_ms = millis();
    needs_redraw = true;
  }
  else if (strcmp(cmd, "BRIGHTNESS") == 0) {
    if (n < 3) return;
    int pct = atoi(tokens[2]);
    if (pct < 0) pct = 0;
    if (pct > 100) pct = 100;
    state.brightness = pct;
    if (dma_display) {
      dma_display->setBrightness8(pct * 255 / 100);
    }
  }
  else if (strcmp(cmd, "THRESHOLDS") == 0) {
    // Reset all domains; missing fields = clear that domain.
    state.bat_thresholds_set = false;
    state.bat_pct_warn_known = false;
    state.alt_thresholds_set = false;
    state.dist_thresholds_set = false;
    state.link_thresholds_set = false;
    state.time_thresholds_set = false;

    // Battery: all four fields required together.
    const char* v_bw = arg_value(tokens, n, 2, "bat_warn");
    const char* v_bc = arg_value(tokens, n, 2, "bat_crit");
    const char* v_bm = arg_value(tokens, n, 2, "bat_min");
    const char* v_bf = arg_value(tokens, n, 2, "bat_full");
    int bat_present = (v_bw != nullptr) + (v_bc != nullptr) + (v_bm != nullptr) + (v_bf != nullptr);
    if (bat_present == 4) {
      state.bat_warn_v = atof(v_bw);
      state.bat_crit_v = atof(v_bc);
      state.bat_min_v  = atof(v_bm);
      state.bat_full_v = atof(v_bf);
      state.bat_thresholds_set = true;
      // Derive percent-band thresholds for the 0-100 BAT bar.
      // bat_pct from telemetry uses an unspecified mapping, but the
      // common convention is pct = (V - min) / (full - min) * 100,
      // so warn/crit voltages map to the same formula. If full <= min
      // we can't derive (bad config), skip silently.
      float span = state.bat_full_v - state.bat_min_v;
      if (span > 0.001f) {
        state.bat_pct_warn = (int)((state.bat_warn_v - state.bat_min_v) / span * 100.0f + 0.5f);
        state.bat_pct_crit = (int)((state.bat_crit_v - state.bat_min_v) / span * 100.0f + 0.5f);
        if (state.bat_pct_warn < 0) state.bat_pct_warn = 0;
        if (state.bat_pct_warn > 100) state.bat_pct_warn = 100;
        if (state.bat_pct_crit < 0) state.bat_pct_crit = 0;
        if (state.bat_pct_crit > 100) state.bat_pct_crit = 100;
        state.bat_pct_warn_known = true;
      }
    } else if (bat_present > 0) {
      send_error("partial battery thresholds");
    }

    // Altitude: warn + crit required together.
    const char* v_aw = arg_value(tokens, n, 2, "alt_warn");
    const char* v_ac = arg_value(tokens, n, 2, "alt_crit");
    if (v_aw && v_ac) {
      state.alt_warn_m = atoi(v_aw);
      state.alt_crit_m = atoi(v_ac);
      state.alt_thresholds_set = true;
    } else if (v_aw || v_ac) {
      send_error("partial altitude thresholds");
    }

    // Distance: warn + crit required together.
    const char* v_dw = arg_value(tokens, n, 2, "dist_warn");
    const char* v_dc = arg_value(tokens, n, 2, "dist_crit");
    if (v_dw && v_dc) {
      state.dist_warn_m = atoi(v_dw);
      state.dist_crit_m = atoi(v_dc);
      state.dist_thresholds_set = true;
    } else if (v_dw || v_dc) {
      send_error("partial distance thresholds");
    }

    // Link: all four fields together (rssi and lq are tightly
    // coupled - half the link picture is misleading).
    const char* v_rw = arg_value(tokens, n, 2, "rssi_warn");
    const char* v_rc = arg_value(tokens, n, 2, "rssi_crit");
    const char* v_lw = arg_value(tokens, n, 2, "lq_warn");
    const char* v_lc = arg_value(tokens, n, 2, "lq_crit");
    int link_present = (v_rw != nullptr) + (v_rc != nullptr) + (v_lw != nullptr) + (v_lc != nullptr);
    if (link_present == 4) {
      state.rssi_warn_dbm = atoi(v_rw);
      state.rssi_crit_dbm = atoi(v_rc);
      state.lq_warn_pct = atoi(v_lw);
      state.lq_crit_pct = atoi(v_lc);
      state.link_thresholds_set = true;
    } else if (link_present > 0) {
      send_error("partial link thresholds");
    }

    // Time: warn + crit required together.
    const char* v_tw = arg_value(tokens, n, 2, "time_warn");
    const char* v_tc = arg_value(tokens, n, 2, "time_crit");
    if (v_tw && v_tc) {
      state.time_warn_s = atoi(v_tw);
      state.time_crit_s = atoi(v_tc);
      state.time_thresholds_set = true;
    } else if (v_tw || v_tc) {
      send_error("partial time thresholds");
    }

    needs_redraw = true;
  }
  else if (strcmp(cmd, "PING") == 0) {
    send_pong();
  }
  // Unknown commands ignored silently per protocol spec.
}

// ===== Outbound messages =====

void send_ready() {
  Serial.print(F("DISP READY version="));
  Serial.print(FW_VERSION);
  Serial.print(F(" panels="));
  Serial.print(PANELS_NUM);
  Serial.print(F(" w="));
  Serial.print(LOGICAL_WIDTH);
  Serial.print(F(" h="));
  Serial.println(LOGICAL_HEIGHT);
}

void send_heartbeat() {
  unsigned long uptime_s = (millis() - boot_ms) / 1000;
  Serial.print(F("DISP HEARTBEAT uptime="));
  Serial.println(uptime_s);
}

void send_pong() {
  Serial.println(F("DISP PONG"));
}

void send_error(const char* msg) {
  Serial.print(F("DISP ERROR \""));
  Serial.print(msg);
  Serial.println(F("\""));
}

// ===== Rendering =====

// Track last rendered mode to detect mode changes. clearScreen() is
// expensive at 10fps (causes visible flicker on big lit areas), so we
// only do it when the mode actually changes. Within flight mode, the
// individual draw functions overwrite their own pixels each frame
// (bars fill before drawing chevron, glyphs redraw same pixels).
static Mode last_rendered_mode = (Mode)-1;

void render() {
  if (!dma_display) return;

  // Only clear on mode transition. Within a mode, the per-element
  // draws fully overwrite their previous output, so clearing is
  // unnecessary and causes flicker.
  if (current_mode != last_rendered_mode) {
    dma_display->clearScreen();
    last_rendered_mode = current_mode;
  }

  switch (current_mode) {
    case Mode::IDLE:       render_idle(); break;
    case Mode::PREFLIGHT:  render_preflight(); break;
    case Mode::FLIGHT:     render_flight(); break;
    case Mode::ALARM:      render_alarm(); break;
    case Mode::RTH:        render_rth(); break;
    case Mode::POSTFLIGHT: render_postflight(); break;
  }
}

// ===== Tile accent palette =====
// Channel ID color palette - primary triad, distinct on RGB LEDs.
#define ACCENT_BAT  0x07E0  // pure green
#define ACCENT_ALT  0x051F  // bright blue (mid-blue, brighter than pure 0x001F)
#define ACCENT_DST  0xFFE0  // pure yellow

// Neutral bar color for ALT/DST when no thresholds defined.
// Medium-bright gray-white. Gets replaced with green/amber/red
// once FC threshold integration lands.
#define COLOR_BAR_NEUTRAL  0x8410  // medium gray

// ===== Animated trend bar =====
//
// Bottom-of-tile bar that conveys trend information through
// chevron animation. Bar is 5 pixels tall (odd height for
// symmetric chevron). Filled in the channel color; a chevron
// shaped void scrolls through it indicating direction of change.
//
// Bar geometry within a tile:
//   x:    tile_x + 2 .. tile_x + width - 2  (38 wide for 42-wide tile)
//   y:    27..31 (5 tall, sits at panel bottom)
//
// Right-pointing chevron void (apex at right edge of void, 5x5):
//   row 0:  ░ █ █ █ █     col 0 only is void
//   row 1:  █ ░ █ █ █     col 1 only
//   row 2:  █ █ ░ █ █     col 2 (apex midline... but we want diagonal)
//
// Actually for a real chevron pointing right, both diagonals
// should converge on a center column at the rightmost. So:
//   row 0:  ░ █ █ █ █     void at col 0
//   row 1:  █ ░ █ █ █     void at col 1
//   row 2:  █ █ ░ █ █     void at col 2 (midpoint)
//   row 3:  █ ░ █ █ █     void at col 1 again
//   row 4:  ░ █ █ █ █     void at col 0
//
// Hmm, that's just a vertical zigzag - not a chevron. For a true
// chevron pointing right, the void shape should be > shaped with
// the apex at the far right and the open side on the left:
//
//   row 0:  ░ █ █ █ █     (top arm: 1 void at left)
//   row 1:  █ ░ █ █ █     (top arm slopes: 1 void second-left)
//   row 2:  █ █ ░ █ █     (apex middle? or further right)
//   row 3:  █ ░ █ █ █     (bottom arm)
//   row 4:  ░ █ █ █ █     (bottom arm)
//
// Wait - this is still wrong. The CHEVRON shape's apex is the
// rightmost POINT. So the void columns by row should be:
//   row 0:  void at col 0
//   row 1:  void at col 1
//   row 2:  void at col 2 (apex - rightmost point)
//   row 3:  void at col 1
//   row 4:  void at col 0
//
// That IS a chevron pointing right. The apex pixel is at row 2 col 2,
// the wings (top and bottom) sweep back to col 0. 5x3 was the
// previous size; widening to 5x5 just gives more visible mass.
// Actually 5x5 with this pattern just makes a thicker chevron, but
// the void itself is still only 1px per row. We want the void to
// be THICKER per row. So the real fix: make void 2px wide per row.

// Right-pointing chevron void with 2px-thick stroke:
//   row 0:  ░ ░ █ █ █     void at cols 0,1
//   row 1:  █ ░ ░ █ █     void at cols 1,2
//   row 2:  █ █ ░ ░ █     void at cols 2,3 (apex band)
//   row 3:  █ ░ ░ █ █     void at cols 1,2
//   row 4:  ░ ░ █ █ █     void at cols 0,1
static const bool CHEVRON_RIGHT[5][5] = {
  {true,  true,  false, false, false},  // row 0
  {false, true,  true,  false, false},  // row 1
  {false, false, true,  true,  false},  // row 2 (apex stripe)
  {false, true,  true,  false, false},  // row 3
  {true,  true,  false, false, false},  // row 4
};
static const bool CHEVRON_LEFT[5][5] = {
  {false, false, false, true,  true},   // row 0
  {false, false, true,  true,  false},  // row 1
  {false, true,  true,  false, false},  // row 2 (apex on left)
  {false, false, true,  true,  false},  // row 3
  {false, false, false, true,  true},   // row 4
};

constexpr int BAR_HEIGHT = 5;
constexpr int BAR_Y = 27;
constexpr int CHEVRON_WIDTH = 5;

// Helper: draw a static fill bar (no animation) for a given x range.
// Used by BAT - just shows percentage in zone color.
static inline void draw_static_bar(int x, int width, int pct, uint16_t color) {
  if (pct < 0) pct = 0;
  if (pct > 100) pct = 100;
  int fill_w = (width * pct) / 100;
  // Background: very dim version of color (1/8 brightness via shift)
  uint16_t bg = (color & 0x18C3); // shift each channel ~3 bits
  dma_display->fillRect(x, BAR_Y, width, BAR_HEIGHT, bg);
  if (fill_w > 0) {
    dma_display->fillRect(x, BAR_Y, fill_w, BAR_HEIGHT, color);
  }
}

// Helper: draw an animated trend bar with scrolling chevron void.
// delta sign determines chevron direction; |delta| determines speed.
// stale: true = no recent updates, animation frozen.
static inline void draw_trend_bar(int x, int width, uint16_t color, int delta, bool stale) {
  // Solid filled bar
  dma_display->fillRect(x, BAR_Y, width, BAR_HEIGHT, color);

  // No chevron when no trend or stale
  if (delta == 0 || stale) return;

  // Speed mapping based on absolute delta. Delta is "change since
  // last sample" (samples ~200ms apart). So 1 m delta over 200ms =
  // 5 m/s. We tier into slow/medium/fast.
  int abs_delta = delta < 0 ? -delta : delta;
  // Frames per chevron-pixel-step. Lower = faster scroll. Doubled
  // from previous (was 1/2/4) so movement reads as deliberate, not
  // frenetic.
  int frames_per_step;
  if (abs_delta < 1)       frames_per_step = 0;  // shouldn't happen (delta==0 caught above)
  else if (abs_delta < 3)  frames_per_step = 8;  // slow climb
  else if (abs_delta < 10) frames_per_step = 4;  // medium
  else                     frames_per_step = 2;  // fast

  // Compute chevron x position from anim_frame and direction.
  // Chevron occupies width = CHEVRON_WIDTH pixels. It scrolls left to
  // right (delta > 0) or right to left (delta < 0), wrapping.
  int travel = width + CHEVRON_WIDTH;  // total scroll distance before wrap
  int step = anim_frame / frames_per_step;
  int pos_in_travel = step % travel;

  int chev_x;
  const bool (*pattern)[5];
  if (delta > 0) {
    // Moving right: chevron starts off-screen left, exits right
    chev_x = x - CHEVRON_WIDTH + pos_in_travel;
    pattern = CHEVRON_RIGHT;
  } else {
    // Moving left: chevron starts off-screen right, exits left
    chev_x = x + width - pos_in_travel;
    pattern = CHEVRON_LEFT;
  }

  // Draw void by setting black pixels where pattern says "void"
  for (int row = 0; row < BAR_HEIGHT; row++) {
    for (int col = 0; col < CHEVRON_WIDTH; col++) {
      if (pattern[row][col]) {
        int px = chev_x + col;
        // Clip to bar bounds
        if (px >= x && px < x + width) {
          dma_display->drawPixel(px, BAR_Y + row, COLOR_BG);
        }
      }
    }
  }
}

// Helper: render text right-aligned within a tile ending at x_right.
static inline void print_right_aligned(const char* s, int x_right, int y_baseline, uint16_t color) {
  int w = u8g2.getUTF8Width(s);
  u8g2.setForegroundColor(color);
  u8g2.setCursor(x_right - w, y_baseline);
  u8g2.print(s);
}

// ===== Custom font rendering =====
//
// Draw one glyph at (x, y) where (x, y) is the top-left of the
// glyph bounding box. Skips dots that fall outside the panel.
static void draw_custom_glyph(int x, int y, int glyph_idx, uint16_t color) {
  if (glyph_idx < 0 || glyph_idx > 11) glyph_idx = 11;
  for (int row = 0; row < CUSTOM_GLYPH_HEIGHT; row++) {
    uint16_t bits = CUSTOM_GLYPHS[glyph_idx][row];
    for (int col = 0; col < CUSTOM_GLYPH_WIDTH; col++) {
      if (bits & (1 << (11 - col))) {
        dma_display->drawPixel(x + col, y + row, color);
      }
    }
  }
}

// Compute the width in pixels of a string rendered with the custom
// font. Each glyph advances CUSTOM_GLYPH_ADVANCE pixels.
static int custom_string_width(const char* s) {
  int n = 0;
  while (*s++) n++;
  if (n == 0) return 0;
  return n * CUSTOM_GLYPH_WIDTH + (n - 1) * (CUSTOM_GLYPH_ADVANCE - CUSTOM_GLYPH_WIDTH);
}

// Draw a string with the custom font, top-left anchored at (x, y).
static void draw_custom_string(const char* s, int x, int y, uint16_t color) {
  while (*s) {
    int idx = custom_glyph_index(*s);
    draw_custom_glyph(x, y, idx, color);
    x += CUSTOM_GLYPH_ADVANCE;
    s++;
  }
}

// Draw a string with the custom font, horizontally centered around
// x_center, top-aligned at y_top.
static void draw_custom_string_centered(const char* s, int x_center, int y_top, uint16_t color) {
  int w = custom_string_width(s);
  draw_custom_string(s, x_center - w / 2, y_top, color);
}

// Draw an integer.fraction value in compressed form: integer in
// the big custom font, fractional digit hugging it in a smaller
// U8g2 font, no decimal point. Optional unit suffix in U8g2 too.
//
// Example: int_part=12, frac_part=7, suffix=nullptr -> "12" + "7"
//          int_part=2,  frac_part=4, suffix="K"     -> "2"  + "4" + "K"
//
// Centered around x_center, top of big glyphs at y_top. Small
// digits sit at bottom-right of the big digits (baseline aligned
// to bottom of the big glyph).
static void draw_custom_decimal_centered(int int_part, int frac_part,
                                         const char* suffix,
                                         int x_center, int y_top,
                                         uint16_t color) {
  char int_buf[8];
  char frac_buf[4];
  snprintf(int_buf, sizeof(int_buf), "%d", int_part);
  snprintf(frac_buf, sizeof(frac_buf), "%d", frac_part);

  // Compute dimmed version of color for fractional digit + suffix.
  // ~35% brightness: take half then subtract eighth = 50% - 12.5% = 37.5%
  uint16_t dim;
  {
    uint8_t r = (color >> 11) & 0x1F;
    uint8_t g = (color >> 5) & 0x3F;
    uint8_t b = color & 0x1F;
    r = (r >> 1) - (r >> 3);
    g = (g >> 1) - (g >> 3);
    b = (b >> 1) - (b >> 3);
    dim = (r << 11) | (g << 5) | b;
  }

  // Measure components
  int w_int = custom_string_width(int_buf);
  u8g2.setFont(u8g2_font_helvB08_tr);
  int w_frac = u8g2.getUTF8Width(frac_buf);
  int w_suffix = (suffix && suffix[0]) ? u8g2.getUTF8Width(suffix) : 0;
  // 1px gap between int and frac, 1px between frac and suffix
  int total = w_int + 1 + w_frac + (w_suffix ? 1 + w_suffix : 0);
  int x = x_center - total / 2;

  // Draw integer in big custom font, full channel color
  draw_custom_string(int_buf, x, y_top, color);
  x += w_int + 1;

  // Draw fraction digit dimmed, baseline aligned to bottom of big glyph
  u8g2.setFont(u8g2_font_helvB08_tr);
  u8g2.setForegroundColor(dim);
  u8g2.setCursor(x, y_top + CUSTOM_GLYPH_HEIGHT);
  u8g2.print(frac_buf);
  x += w_frac;

  // Suffix to the right of fraction, also dimmed
  if (w_suffix) {
    x += 1;
    u8g2.setCursor(x, y_top + CUSTOM_GLYPH_HEIGHT);
    u8g2.print(suffix);
  }
}


// IDLE: dim "ZEROTX" centered.
void render_idle() {
  u8g2.setFont(FONT_TEXT);
  // Dim version of label color so it whispers rather than shouts.
  u8g2.setForegroundColor(dma_display->color565(0, 50, 40));
  const char* msg = "ZEROTX";
  int w = u8g2.getUTF8Width(msg);
  u8g2.setCursor((LOGICAL_WIDTH - w) / 2, 22);
  u8g2.print(msg);
}

// PREFLIGHT: amber "PREFLIGHT" header with GPS/SATS status below.
void render_preflight() {
  u8g2.setFont(FONT_SMALL);
  u8g2.setForegroundColor(COLOR_CAUTION);
  u8g2.setCursor(2, 11);
  u8g2.print("PREFLIGHT");

  u8g2.setForegroundColor(COLOR_LABEL);
  if (state.gps_fix.length() > 0) {
    u8g2.setCursor(2, 24);
    u8g2.print("GPS ");
    u8g2.setForegroundColor(COLOR_VALUE);
    u8g2.print(state.gps_fix.c_str());
  }
  if (state.sats_known) {
    u8g2.setForegroundColor(COLOR_LABEL);
    u8g2.setCursor(60, 24);
    u8g2.print("SATS ");
    u8g2.setForegroundColor(COLOR_VALUE);
    u8g2.print(state.sats);
  }
}

// (Stale comment block from prior round, kept for context only)
// FLIGHT: the showpiece. Three tiles, each marked by a 2px colored
// FLIGHT layout:
//
// Three tiles (BAT/ALT/DST) showing primary telemetry. Tile widths:
// 0..41, 43..84, 86..127 (1-col gaps between). Channel ID is encoded
// in digit color: BAT=green, ALT=blue, DST=yellow.
//
// Layout y values (panel is 32 tall):
//   y=1..25   custom 7-segment glyphs (25px tall) for big values
//   y=18..25  small fractional digit + unit suffix (helvB08, dimmed)
//             baseline-aligned to bottom of big glyph
//   y=27..31  5px trend bar at bottom (chevron-animated for ALT/DST,
//             color: BAT zone-coded green/amber/red, ALT/DST gray
//             until FC threshold integration lands)
void render_flight() {
  // No top accent stripe - channel ID is encoded in digit color now.

  // Track previously-rendered values so we can clear digit area only
  // when the displayed value actually changes. Animation-tick redraws
  // (10fps) skip the clear since the digits are unchanged - they just
  // overdraw their existing pixels, which is invisible. This avoids
  // the flicker that comes from clearing then redrawing big lit
  // areas at 10fps.
  static int last_alt_m = INT32_MIN;
  static int last_dist_m = INT32_MIN;
  static int last_bat_x10 = INT32_MIN;  // voltage * 10 (rounded)
  static int last_bat_pct = INT32_MIN;
  static bool last_bat_known = false;
  static bool last_alt_known = false;
  static bool last_dist_known = false;

  int cur_bat_x10 = state.bat_known ? (int)(state.bat_v * 10.0f + 0.5f) : INT32_MIN;
  bool digits_changed =
       (cur_bat_x10 != last_bat_x10)
    || (state.alt_m != last_alt_m)
    || (state.dist_m != last_dist_m)
    || (state.bat_known != last_bat_known)
    || (state.alt_known != last_alt_known)
    || (state.dist_known != last_dist_known);

  if (digits_changed) {
    // Clear above-bar area so old digits don't ghost
    dma_display->fillRect(0, 0, LOGICAL_WIDTH, 27, COLOR_BG);
    last_bat_x10 = cur_bat_x10;
    last_alt_m = state.alt_m;
    last_dist_m = state.dist_m;
    last_bat_known = state.bat_known;
    last_alt_known = state.alt_known;
    last_dist_known = state.dist_known;
  }

  // Note: bat_pct doesn't need a clear since the bar is fully
  // overwritten by draw_static_bar each frame; just track for
  // completeness.
  last_bat_pct = state.bat_pct;
  (void)last_bat_pct;

  // Stale-data flags. If state hasn't updated for >2s, freeze
  // animation (chevron disappears, bar shows last value).
  unsigned long now = millis();
  bool alt_stale = !state.alt_known || (now - state.alt_updated_ms) > TREND_STALE_MS;
  bool dist_stale = !state.dist_known || (now - state.dist_updated_ms) > TREND_STALE_MS;

  // ============== BAT tile (x: 0..41, center=21) ==============
  // Voltage as integer.tenths in BAT channel color.
  if (state.bat_known) {
    int volts_x10 = (int)(state.bat_v * 10.0f + 0.5f);
    int int_part = volts_x10 / 10;
    int frac_part = volts_x10 % 10;
    draw_custom_decimal_centered(int_part, frac_part, nullptr, 21, 1, ACCENT_BAT);
  } else {
    draw_custom_string_centered("--", 21, 1, COLOR_LABEL);
  }

  // BAT bar: green/amber/red zones. Thresholds come from daemon-pushed
  // values when available, else fall back to hardcoded 50/20/10.
  if (state.batpct_known) {
    int warn_pct, crit_pct, blink_pct;
    if (state.bat_pct_warn_known) {
      warn_pct  = state.bat_pct_warn;
      crit_pct  = state.bat_pct_crit;
      blink_pct = state.bat_pct_crit / 2;  // half of crit for the panic blink
      if (blink_pct < 5) blink_pct = 5;
    } else {
      warn_pct  = 50;
      crit_pct  = 20;
      blink_pct = 10;
    }
    uint16_t color;
    if (state.bat_pct > warn_pct)      color = COLOR_OK;
    else if (state.bat_pct > crit_pct) color = COLOR_CAUTION;
    else                               color = COLOR_CRITICAL;
    bool show = true;
    if (state.bat_pct < blink_pct) {
      // anim_frame ticks at 100ms (10fps). 5 frames on, 5 frames off = 1Hz blink.
      show = (anim_frame / 5) % 2 == 0;
    }
    if (show) {
      draw_static_bar(0, 42, state.bat_pct, color);
    }
  }

  // ============== ALT tile (x: 43..85, center=64) ==============
  // Altitude is integer meters, rendered in ALT channel color.
  if (state.alt_known) {
    char buf[8];
    snprintf(buf, sizeof(buf), "%d", state.alt_m);
    draw_custom_string_centered(buf, 64, 1, ACCENT_ALT);
  } else {
    draw_custom_string_centered("--", 64, 1, COLOR_LABEL);
  }

  // ALT trend bar: animated chevron. Color zones from threshold if set,
  // neutral gray otherwise.
  if (state.alt_known) {
    uint16_t alt_bar_color = COLOR_BAR_NEUTRAL;
    if (state.alt_thresholds_set) {
      if      (state.alt_m >= state.alt_crit_m) alt_bar_color = COLOR_CRITICAL;
      else if (state.alt_m >= state.alt_warn_m) alt_bar_color = COLOR_CAUTION;
      else                                      alt_bar_color = COLOR_OK;
    }
    draw_trend_bar(43, 42, alt_bar_color, state.alt_delta, alt_stale);
  }

  // ============== DST tile (x: 86..127, center=107) ==============
  // Distance: <1000 = meters integer, >=1000 = km with decimal.
  // Always in DST channel color.
  if (state.dist_known) {
    if (state.dist_m >= 1000) {
      // Render as kilometers with 1 decimal: 1500 -> "1" + small "5" + "K"
      int km_x10 = (state.dist_m + 50) / 100;
      int int_part = km_x10 / 10;
      int frac_part = km_x10 % 10;
      draw_custom_decimal_centered(int_part, frac_part, "K", 107, 1, ACCENT_DST);
    } else {
      char buf[8];
      snprintf(buf, sizeof(buf), "%d", state.dist_m);
      draw_custom_string_centered(buf, 107, 1, ACCENT_DST);
    }
  } else {
    draw_custom_string_centered("--", 107, 1, COLOR_LABEL);
  }

  // DST trend bar: animated chevron. Color zones from threshold if set,
  // neutral gray otherwise.
  if (state.dist_known) {
    uint16_t dst_bar_color = COLOR_BAR_NEUTRAL;
    if (state.dist_thresholds_set) {
      if      (state.dist_m >= state.dist_crit_m) dst_bar_color = COLOR_CRITICAL;
      else if (state.dist_m >= state.dist_warn_m) dst_bar_color = COLOR_CAUTION;
      else                                        dst_bar_color = COLOR_OK;
    }
    draw_trend_bar(86, 42, dst_bar_color, state.dist_delta, dist_stale);
  }
}

// ALARM: full-width banner. Color = level. Big text dominates;
// no tile separators (alarm is not a data cluster).
void render_alarm() {
  uint16_t bg, fg;
  if (state.alarm_level == "critical") {
    bg = dma_display->color565(80, 0, 0);
    fg = dma_display->color565(255, 80, 80);
  } else if (state.alarm_level == "warning") {
    bg = dma_display->color565(60, 30, 0);
    fg = COLOR_CAUTION;
  } else {
    bg = COLOR_BG;
    fg = COLOR_VALUE;
  }
  dma_display->fillRect(0, 0, LOGICAL_WIDTH, LOGICAL_HEIGHT, bg);

  u8g2.setFont(FONT_SMALL);
  u8g2.setForegroundColor(fg);
  u8g2.setCursor(2, 11);
  u8g2.print("ALARM");

  // Truncate alarm text to fit at small font width (~24 chars at 5px wide).
  String t = state.alarm_text;
  if (t.length() > 24) t = t.substring(0, 24);
  u8g2.setCursor(2, 26);
  u8g2.print(t.c_str());
}

// RTH: green palette, distance prominent, "HOME" label.
// Round 1 has no compass arrow (needs bearing data); the layout
// reserves room on the left for an arrow in a future round.
void render_rth() {
  u8g2.setFont(FONT_SMALL);
  u8g2.setForegroundColor(COLOR_OK);
  u8g2.setCursor(2, 11);
  u8g2.print("RTH HOME");

  // FONT_TEXT (full ASCII) for the value because we append K or M.
  u8g2.setFont(FONT_TEXT);
  if (state.dist_known) {
    char buf[12];
    if (state.dist_m >= 1000) {
      snprintf(buf, sizeof(buf), "%.1fK", state.dist_m / 1000.0f);
    } else {
      snprintf(buf, sizeof(buf), "%dM", state.dist_m);
    }
    print_right_aligned(buf, 126, 28, COLOR_VALUE);
  }
}

// POSTFLIGHT: time + peak altitude, calm presentation.
void render_postflight() {
  u8g2.setFont(FONT_SMALL);
  u8g2.setForegroundColor(COLOR_LABEL);
  u8g2.setCursor(2, 11);
  u8g2.print("FLIGHT");
  u8g2.setForegroundColor(COLOR_VALUE);
  u8g2.print(" END");

  if (state.time_known) {
    char buf[12];
    int m = state.time_s / 60;
    int s = state.time_s % 60;
    snprintf(buf, sizeof(buf), "%d:%02d", m, s);
    u8g2.setForegroundColor(COLOR_LABEL);
    u8g2.setCursor(2, 24);
    u8g2.print("TIME ");
    u8g2.setForegroundColor(COLOR_VALUE);
    u8g2.print(buf);
  }
  if (state.alt_known) {
    char buf[8];
    snprintf(buf, sizeof(buf), "%dM", state.alt_m);
    u8g2.setForegroundColor(COLOR_LABEL);
    u8g2.setCursor(70, 24);
    u8g2.print("PEAK ");
    u8g2.setForegroundColor(COLOR_VALUE);
    u8g2.print(buf);
  }
}
