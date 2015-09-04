package main

import (
	"io"
	"io/ioutil"
	"os/exec"
	"strings"
	"testing"
	"time"
)

var testPort = "25353"

type testZone struct {
	LastModified time.Time
	Contents     string
}
type testGetter struct {
	testZones map[string]testZone
}

func (g testGetter) ListZones() ([]zoneFile, error) {
	zones := []zoneFile{}
	for key, z := range g.testZones {
		zones = append(zones, zoneFile{Key: key, LastModified: z.LastModified})
	}
	return zones, nil
}

func (s testGetter) GetZone(zoneName string) (io.ReadCloser, error) {
	r := strings.NewReader(s.testZones[zoneName].Contents)
	return ioutil.NopCloser(r), nil
}

func TestGet(t *testing.T) {
	c := config{}
	getter := testGetter{testZones: map[string]testZone{
		"abc.com":    testZone{LastModified: time.Now().AddDate(-1, 0, 0), Contents: abcZone},
		"def.com":    testZone{LastModified: time.Now().AddDate(0, 0, -1), Contents: defZone},
		"recent.com": testZone{LastModified: time.Now(), Contents: defZone},
	}}

	z, err := c.getZones(getter)
	if err != nil {
		t.Errorf("getZones failed: %s", err.Error())
	}
	if len(z) != 3 {
		t.Errorf("getZones returned wrong # of zones (got: %d, wanted: %d)", len(z), 3)
	}
	if !strings.Contains(z["def.com"], "nsa.def.com.") {
		t.Errorf("getZones returned wrong zone contents (%s missing %s)", "def.com", "nsa.def.com.")
	}
	if strings.Contains(z["abc.com"], "nsa.def.com.") {
		t.Errorf("getZones returned wrong zone contents (%s has %s)", "abc.com", "nsa.def.com.")
	}

	recent := getter.testZones["recent.com"]
	recent.LastModified = time.Now()
	getter.testZones["recent.com"] = recent
	z2, err := c.getZones(getter)
	if err != nil {
		t.Errorf("getZones failed on try 2: %s", err.Error())
	}
	if len(z2) != 1 {
		t.Errorf("getZones returned wrong # of zones (got: %d, wanted: %d)", len(z), 1)
	}
}

var abcZone = `$TTL    300
$ORIGIN .
abc.com 	86400    IN      SOA     nsa.abc.com. admin.abc.com. ( 2014121700 10800 1200 864000 7200 )
        	IN      NS      nsa.abc.com.
        	IN      NS      nsb.abc.com.
        	IN      MX	10 mail.abc.com.
$ORIGIN abc.com.
		IN	A	127.0.0.1
www		IN	CNAME	abc.com.
`

var defZone = `$TTL    300
$ORIGIN .
def.com 	86400    IN      SOA     nsa.def.com. admin.def.com. ( 2014121700 10800 1200 864000 7200 )
        	IN      NS      nsa.def.com.
        	IN      NS      nsb.def.com.
        	IN      MX	10 mail.def.com.
$ORIGIN def.com.
		IN	A	127.0.0.2
www		IN	CNAME	def.com.
`

var flatZone = `$TTL    300
$ORIGIN .
flat.com 	86400    IN      SOA     nsa.flat.com. admin.flat.com. ( 2014121700 10800 1200 864000 7200 )
        	IN      NS      nsa.flat.com.
        	IN      NS      nsb.flat.com.
        	IN      MX	10 mail.flat.com.
$ORIGIN flat.com.
		IN	CNAME	def.com.
www		IN	CNAME	flat.com.
`

func TestServe(t *testing.T) {
	c := config{resolver: "127.0.0.1:" + testPort}
	getter := testGetter{testZones: map[string]testZone{
		"abc.com":  testZone{LastModified: time.Now().AddDate(-1, 0, 0), Contents: abcZone},
		"def.com":  testZone{LastModified: time.Now().AddDate(0, 0, -1), Contents: defZone},
		"flat.com": testZone{LastModified: time.Now(), Contents: flatZone},
	}}
	z, err := c.getZones(getter)
	if err != nil {
		t.Errorf("getZones failed: %s", err.Error())
	}
	if err := c.loadZones(z); err != nil {
		t.Errorf("loadZones failed: %s", err.Error())
	}
	startServer(testPort)

	cmd := exec.Command("dig", "-p", testPort, "@localhost", "abc.com")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "127.0.0.1") {
		t.Errorf("basic dig failed: want: %s, got: %v", "127.0.0.1", string(out))
	}
	cmd = exec.Command("dig", "-p", testPort, "@localhost", "def.com")
	out, _ = cmd.CombinedOutput()
	if !strings.Contains(string(out), "127.0.0.2") {
		t.Errorf("basic dig failed: want: %s, got: %v", "127.0.0.2", string(out))
	}
	cmd = exec.Command("dig", "-p", testPort, "@localhost", "jkl.com")
	out, _ = cmd.CombinedOutput()
	if !strings.Contains(string(out), "QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 0") {
		t.Errorf("invalid dig failed: want: %s, got: %v", "QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 0", string(out))
	}
	cmd = exec.Command("dig", "-p", testPort, "@localhost", ".", "TXT")
	out, _ = cmd.CombinedOutput()
	if !strings.Contains(string(out), version) {
		t.Errorf("version dig failed: want: %s, got: %v", version, string(out))
	}
	if !strings.Contains(string(out), "NedDNS") {
		t.Errorf("version dig failed: want: %s, got: %v", "NedDNS", string(out))
	}

	cmd = exec.Command("dig", "-p", testPort, "@localhost", "flat.com")
	out, _ = cmd.CombinedOutput()
	if !strings.Contains(string(out), "127.0.0.2") {
		t.Errorf("flat dig failed: want: %s, got: %v", "127.0.0.2", string(out))
	}
	cmd = exec.Command("dig", "-p", testPort, "@localhost", "flat.com", "CNAME")
	out, _ = cmd.CombinedOutput()
	if !strings.Contains(string(out), "def.com.") {
		t.Errorf("flat dig cname failed: want: %s, got: %v", "opsbot.com.", string(out))
	}

}
