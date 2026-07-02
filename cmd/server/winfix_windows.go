//go:build windows

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package main

// Blank import pulls in the emutls linker stub required to link the
// prebuilt duckdb windows static libs with mingw gcc >= 13 toolchains.
import _ "github.com/kuroky/claude-code-monitor/internal/winfix"
