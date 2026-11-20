//go:build !linux

package filevolume

import (
	"fmt"
	"time"
)

type FDRegistry struct{}

var NativeFDs FDRegistry

func (*FDRegistry) Has(string, string) bool { return false }
func (*FDRegistry) Offer(string, string, string, string, int) error {
	return fmt.Errorf("file fd handoff requires Linux")
}
func (*FDRegistry) OfferLazy(string, string, string, string, func() (int, error)) error {
	return fmt.Errorf("file fd handoff requires Linux")
}
func (*FDRegistry) Close(string) bool { return false }
func (*FDRegistry) ClosePod(string)   {}
func ReceiveFD(string, string, string, time.Duration) (int, error) {
	return -1, fmt.Errorf("file fd handoff requires Linux")
}
