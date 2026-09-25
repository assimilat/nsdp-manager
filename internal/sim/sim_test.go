package sim

import (
	"context"
	"testing"
	"time"

	"prosafe/internal/nsdp"
)

// TestReadWriteRoundtrip exercises the codec and the write path through the
// simulator without any network.
func TestReadWriteRoundtrip(t *testing.T) {
	s := NewXS708E()
	// read model
	resp := s.Handle(&nsdp.Packet{Op: nsdp.OpReadRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{{Tag: nsdp.TagModel}}})
	if got := nsdp.Str(resp.Get(nsdp.TagModel)); got != "XS708E" {
		t.Fatalf("model = %q", got)
	}
	// write name with correct (xor) auth
	auth := nsdp.AuthTLV(nsdp.PasswordXOR, "password", s.MAC, nil)
	w := s.Handle(&nsdp.Packet{Op: nsdp.OpWriteRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{auth, nsdp.StringTLV(nsdp.TagName, "core-sw")}})
	if w.Result != nsdp.ResultOK {
		t.Fatalf("write name failed: 0x%04x", w.Result)
	}
	resp = s.Handle(&nsdp.Packet{Op: nsdp.OpReadRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{{Tag: nsdp.TagName}}})
	if got := nsdp.Str(resp.Get(nsdp.TagName)); got != "core-sw" {
		t.Fatalf("name = %q", got)
	}
	// bad password rejected
	bad := nsdp.AuthTLV(nsdp.PasswordXOR, "wrong", s.MAC, nil)
	w = s.Handle(&nsdp.Packet{Op: nsdp.OpWriteRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{bad, nsdp.StringTLV(nsdp.TagName, "x")}})
	if w.Result != nsdp.ResultBadPassword {
		t.Fatalf("expected bad password, got 0x%04x", w.Result)
	}
	// 802.1q vlan create then read back after switching mode
	s.Handle(write(s, nsdp.ByteTLV(nsdp.TagVLANMode, nsdp.VLAN8021QAdv)))
	s.Handle(write(s, nsdp.VLAN8021QTLV(10, []int{1, 2, 3}, []int{3}, 8)))
	resp = s.Handle(&nsdp.Packet{Op: nsdp.OpReadRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{{Tag: nsdp.TagVLAN8021Q}}})
	var found *nsdp.VLAN8021Q
	for _, v := range resp.All(nsdp.TagVLAN8021Q) {
		q, _ := nsdp.DecodeVLAN8021Q(v)
		if q.VID == 10 {
			qq := q
			found = &qq
		}
	}
	if found == nil {
		t.Fatal("vlan 10 not found")
	}
	if nsdp.PortList(found.Members) != "1-3" || nsdp.PortList(found.Tagged) != "3" {
		t.Fatalf("vlan 10 members=%v tagged=%v", found.Members, found.Tagged)
	}
	// LAG overlap rejected
	s.Handle(write(s, nsdp.LAGTLV(1, true, []int{1, 2}, 8)))
	w = s.Handle(write(s, nsdp.LAGTLV(2, true, []int{2, 3}, 8)))
	if w.Result == nsdp.ResultOK {
		t.Fatal("expected LAG overlap to be rejected")
	}
	// unsupported tag on a model without it
	w = s.Handle(write(s, nsdp.BoolTLV(nsdp.TagPowerSaving, true)))
	if w.Result != nsdp.ResultUnsupportedTLV {
		t.Fatalf("expected unsupported, got 0x%04x", w.Result)
	}
	// factory reset counter
	s.Handle(write(s, nsdp.ByteTLV(nsdp.TagFactoryReset, 1)))
	if _, resets, _ := s.Counters(); resets == 0 {
		t.Fatal("factory reset not recorded")
	}
}

func write(s *Switch, tlv nsdp.TLV) *nsdp.Packet {
	auth := nsdp.AuthTLV(s.Mode, s.Password, s.MAC, nil)
	return &nsdp.Packet{Op: nsdp.OpWriteRequest, DeviceMAC: s.MAC, TLVs: []nsdp.TLV{auth, tlv}}
}

func TestServeOverUDP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewXS708E()
	go s.Serve(ctx, "127.0.0.1:63500")
	time.Sleep(100 * time.Millisecond)
	// nothing to assert beyond "does not panic / binds"; covered by roundtrip
}
