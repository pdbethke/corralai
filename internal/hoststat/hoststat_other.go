// SPDX-License-Identifier: Elastic-2.0

//go:build !linux

package hoststat

import "errors"

// supported is false off Linux: no reader is written for this platform, so
// every figure is NotMeasured ("not measured on <GOOS>") rather than guessed.
const supported = false

var errUnsupported = errors.New("hoststat: platform not supported")

func readLoad(string) (float64, error)       { return 0, errUnsupported }
func readMemAvailable(string) (int64, error) { return 0, errUnsupported }
func usage() (int64, int64, error)           { return 0, 0, errUnsupported }
