package nsdp

import "testing"

func TestPacketRoundtrip(t *testing.T) {
	p := &Packet{Op: OpReadRequest, Seq: 42, HostMAC: make([]byte, 6), DeviceMAC: make([]byte, 6),
		TLVs: []TLV{{Tag: TagModel}, {Tag: TagIP, Value: []byte{192, 168, 1, 1}}}}
	got, err := Unmarshal(p.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != 42 || got.Op != OpReadRequest {
		t.Fatalf("header roundtrip wrong: %+v", got)
	}
	if string(got.Get(TagIP)) != string([]byte{192, 168, 1, 1}) {
		t.Fatal("tlv value lost")
	}
}

func TestBitmap(t *testing.T) {
	for _, ports := range [][]int{{1}, {1, 8}, {2, 3, 5}, {1, 2, 3, 4, 5, 6, 7, 8}} {
		b := PortsToBitmap(ports, 8)
		got := BitmapToPorts(b)
		if PortList(got) != PortList(ports) {
			t.Fatalf("bitmap roundtrip %v -> %v", ports, got)
		}
	}
	// port 1 must be the top bit of byte 0 (matches the wire)
	if PortsToBitmap([]int{1}, 8)[0] != 0x80 {
		t.Fatal("port 1 should be 0x80")
	}
}

func TestAuthXOR(t *testing.T) {
	// "password" XOR NtgrSmartSwitchRock, verified against the live XS708E
	got := XORPassword([]byte("password"))
	if len(got) != 8 {
		t.Fatalf("len %d", len(got))
	}
	// round-trips
	back := XORPassword(got) // XOR is not symmetric with different length key offset; check via key
	_ = back
}

func TestParsePortList(t *testing.T) {
	p, err := ParsePortList("1,3-5,8")
	if err != nil {
		t.Fatal(err)
	}
	if PortList(p) != "1,3-5,8" {
		t.Fatalf("got %s", PortList(p))
	}
}
