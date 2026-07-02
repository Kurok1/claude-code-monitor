//go:build windows

/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since v2.2.0
 */

package winfix

// import "C" activates cgo so once_stub_windows.cpp is compiled into any
// windows binary that (blank-)imports this package.
import "C"
