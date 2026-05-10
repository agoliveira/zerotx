cmake_minimum_required(VERSION 3.13)

# PICO_SDK_PATH must be in env, or set CMake variable PICO_SDK_PATH.
include(pico_sdk_import.cmake)

project(zerotx-fw C CXX ASM)
set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 17)

pico_sdk_init()

add_executable(zerotx-fw
    src/main.c
    src/ipc.c
    src/crsf.c
    src/state.c
    src/status_led.c
    src/input_arm.c
    src/input_momentary.c
)

target_include_directories(zerotx-fw PRIVATE src)

# Generate ws2812.pio.h from the .pio source.
pico_generate_pio_header(zerotx-fw ${CMAKE_CURRENT_LIST_DIR}/src/ws2812.pio)

target_link_libraries(zerotx-fw
    pico_stdlib
    hardware_uart
    hardware_pio
    hardware_watchdog
)

# USB CDC carries the IPC protocol; UART stdio off (UART0 is for CRSF).
pico_enable_stdio_usb(zerotx-fw 1)
pico_enable_stdio_uart(zerotx-fw 0)

# Produce uf2/elf/bin/etc.
pico_add_extra_outputs(zerotx-fw)
