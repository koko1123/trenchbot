package filter

import "testing"

func TestBundleDetector_CreatorInEarlyBuyers(t *testing.T) {
	bd := NewBundleDetector(true)
	creator := "CreatorAddr111111111111111111111111111111"
	earlyBuyers := []string{
		"BuyerA1111111111111111111111111111111111111",
		creator,
		"BuyerB1111111111111111111111111111111111111",
	}

	if !bd.IsBundled(creator, earlyBuyers) {
		t.Fatal("expected bundled when creator is among early buyers")
	}
}

func TestBundleDetector_CreatorNotInEarlyBuyers(t *testing.T) {
	bd := NewBundleDetector(true)
	creator := "CreatorAddr111111111111111111111111111111"
	earlyBuyers := []string{
		"BuyerA1111111111111111111111111111111111111",
		"BuyerB1111111111111111111111111111111111111",
	}

	if bd.IsBundled(creator, earlyBuyers) {
		t.Fatal("expected not bundled when creator is not among early buyers")
	}
}

func TestBundleDetector_MultiBuyFromSameAddress(t *testing.T) {
	bd := NewBundleDetector(true)
	creator := "CreatorAddr111111111111111111111111111111"
	repeater := "BotAddr11111111111111111111111111111111111"
	earlyBuyers := []string{
		repeater,
		"BuyerA1111111111111111111111111111111111111",
		repeater,
		"BuyerB1111111111111111111111111111111111111",
		repeater,
	}

	if !bd.IsBundled(creator, earlyBuyers) {
		t.Fatal("expected bundled when single address appears 3+ times")
	}
}

func TestBundleDetector_Disabled(t *testing.T) {
	bd := NewBundleDetector(false)
	creator := "CreatorAddr111111111111111111111111111111"
	earlyBuyers := []string{creator}

	if bd.IsBundled(creator, earlyBuyers) {
		t.Fatal("expected not bundled when detector is disabled")
	}
}

func TestBundleDetector_EmptyEarlyBuyers(t *testing.T) {
	bd := NewBundleDetector(true)
	if bd.IsBundled("creator", nil) {
		t.Fatal("expected not bundled with empty early buyers")
	}
}
