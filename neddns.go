// Licensed under terms of MIT license, Copyright (c) 2015, ned@appliedtrust.com
package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/docopt/docopt-go"
	"github.com/miekg/dns"
	"github.com/quipo/statsd"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "0.1.2015111500"

var usage = `neddns: simple authoratative DNS server backed by S3

Usage:
	neddns [options] <bucket>
	neddns -h --help
	neddns --version

AWS Authentication:
  Either use the -K and -S flags, or
  set the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.

Options:
  -K, --awskey=<keyid>      AWS key ID (or use AWS_ACCESS_KEY_ID environemnt variable).
  -S, --awssecret=<secret>  AWS secret key (or use AWS_SECRET_ACCESS_KEY environemnt variable).
  -R, --region=<region>     AWS region [default: us-east-1].
  -u, --update=<secs>       Frequency to fetch updated zones from S3 in seconds [default: 300].
  -p, --port=<port>         Listen port [default: 53].
  -f, --prefix=<prefix>     AWS object prefix (such as directory name).
  -r, --resolver=<host:port>	DNS resolver for CNAME flattening [default: 8.8.8.8:53].
  -l, --log=<path>          Write to file at this loctation rather than stdout.
  --statsd_server=<host:port>	Statsd server and port - statsd is disabled if empty.
  --statsd_prefix=<prefix>		Prefix to add to statsd metrics [default: neddns].
  -d, --debug               Enable debugging output.
  -h, --help                Show this screen.
  --version                 Show version.
`

type zone struct {
	name string
	rrs  []dns.RR
}

type config struct {
	awsKeyId     string
	awsSecret    string
	bucket       string
	port         string
	logfile      string
	region       string
	prefix       string
	resolver     string
	debugOn      bool
	lastUpdate   time.Time
	update       time.Duration
	statsdServer string
	statsdPrefix string
	stats        statsd.Statsd
}

func main() {
	c, err := parseArgs()
	if err != nil {
		log.Fatalf("Error parsing arguments: %s", err.Error())
	}

	if len(c.logfile) > 0 {
		logfile, err := os.OpenFile(c.logfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("Error opening log file %s: %v", c.logfile, err)
		}
		defer logfile.Close()
		log.SetOutput(logfile)
	}
	if len(c.statsdServer) > 0 {
		c.stats = statsd.NewStatsdClient(c.statsdServer, c.statsdPrefix)
		c.stats.CreateSocket()
		c.debug("Statsd enabled.")
	} else {
		c.stats = statsd.NoopClient{}
	}

	getter := s3getter{region: c.region, bucket: c.bucket, prefix: c.prefix}
	c.debug("Fetching zones...")
	z, err := c.getZones(getter)
	if err != nil {
		log.Fatal(err)
	}
	c.stats.Gauge("zones", int64(len(z)))
	c.debug(fmt.Sprintf("Fetched %d zones...", len(z)))

	c.debug("Loading zones...")
	err = c.loadZones(z)
	if err != nil {
		log.Fatal(err)
	}
	c.registerVersionHandler()
	c.debug("Starting server...")
	c.startServer()
	log.Printf("DNS server running on TCP/UDP port %s (v%s)", c.port, version)
	c.stats.Incr("started", 1)

	doUpdate := make(chan bool)
	go func() {
		for {
			select {
			case <-doUpdate:
				c.debug("Update signal... fetching updating zones")
			case <-time.After(c.update):
				c.debug("Update timeout... fetching updating zones")
			}
			z, err := c.getZones(getter)
			if err != nil {
				log.Fatal(err)
			}
			c.debug(fmt.Sprintf("Fetched %d updated zones", len(z)))
			if len(z) > 0 {
				c.stats.Incr("zoneupdates", int64(len(z)))
				c.debug(fmt.Sprintf("Reloading %d zones now", len(z)))
				err = c.loadZones(z)
				if err != nil {
					log.Fatal(err)
				}
			}
			c.debug("Updated zones successfully")
		}
	}()

	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case s := <-sig:
			if s == syscall.SIGHUP {
				doUpdate <- true
			} else {
				log.Fatalf("Signal (%d) received, stopping", s)
			}
		}
	}
}

// type zoneGetter interface abstracts calls to AWS S3
type zoneGetter interface {
	ListZones() ([]zoneFile, error)
	GetZone(string) (io.ReadCloser, error)
}

type zoneFile struct {
	Key          string
	LastModified time.Time
}

func (c *config) getZones(getter zoneGetter) (map[string]string, error) {
	zones := map[string]string{}
	resp, err := getter.ListZones()
	if err != nil {
		return zones, err
	}
	for _, k := range resp {
		if k.Key == c.prefix {
			continue
		}
		if k.LastModified.Before(c.lastUpdate.Add(-1 * time.Minute)) { // accomodate clock skew
			continue
		}
		zoneData, err := getter.GetZone(k.Key)
		if err != nil {
			return zones, err
		}
		b, err := ioutil.ReadAll(zoneData)
		if err != nil {
			return zones, err
		}
		zones[strings.TrimPrefix(k.Key, c.prefix)] = string(b)
	}
	c.lastUpdate = time.Now()
	return zones, nil
}

func (c *config) loadZones(zones map[string]string) error {
	for n, f := range zones {
		c.debug(fmt.Sprintf("Parsing zone %s", n))
		z := zone{name: n, rrs: []dns.RR{}}
		for t := range dns.ParseZone(strings.NewReader(f), n, n) {
			if t.Error != nil {
				log.Fatalf("Error parsing zone %s: %s", n, t.Error)
			}
			z.rrs = append(z.rrs, t.RR)
		}
		dns.HandleFunc(n, func(w dns.ResponseWriter, req *dns.Msg) {
			z.zoneHandler(c, w, req)
		})
		c.debug(fmt.Sprintf("Registered handler for zone %s", n))
	}
	return nil
}

func (z *zone) zoneHandler(c *config, w dns.ResponseWriter, req *dns.Msg) {
	c.stats.Incr("query.request", 1)
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.Answer = []dns.RR{}
	questions := []string{}
	answers := []string{}
	if len(req.Question) != 1 {
		c.stats.Incr("query.error", 1)
		log.Printf("Warning: len(req.Question) != 1")
		return
	}
	q := req.Question[0]
	questions = append(questions, fmt.Sprintf("%s[%s]", q.Name, dns.TypeToString[q.Qtype]))
	if q.Qclass != uint16(dns.ClassINET) {
		c.stats.Incr("query.error", 1)
		log.Printf("Warning: skipping unhandled class: %s", dns.ClassToString[q.Qclass])
		return
	}
	for _, record := range z.rrs {
		h := record.Header()
		if q.Name != h.Name {
			continue
		}
		txt := record.String()
		if q.Qtype == dns.TypeA && h.Rrtype == dns.TypeCNAME { // special handling for A queries w/CNAME results
			if q.Name == dns.Fqdn(z.name) { // flatten root CNAME
				flat, err := c.flattenCNAME(record.(*dns.CNAME))
				if err != nil || flat == nil {
					log.Printf("flattenCNAME error: %s", err.Error())
				} else {
					for _, record := range flat {
						m.Answer = append(m.Answer, record)
						answers = append(answers, "(FLAT)"+record.String())
					}
				}
				continue
			} // don't flatten other CNAMEs for now
		} else if q.Qtype != h.Rrtype && q.Qtype != dns.TypeANY { // skip RRs that don't match
			continue
		}
		m.Answer = append(m.Answer, record)
		answers = append(answers, txt)
	}
	//m.Extra = []dns.RR{}
	//m.Extra = append(m.Extra, &dns.TXT{Hdr: dns.RR_Header{Name: m.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: []string{"DNS rocks"}})
	c.debug(fmt.Sprintf("Query [%s] %s -> %s ", w.RemoteAddr().String(), strings.Join(questions, ","), strings.Join(answers, ",")))
	c.stats.Incr("query.answer", 1)

	w.WriteMsg(m)
}

func (c *config) flattenCNAME(in *dns.CNAME) ([]dns.RR, error) { // TODO: cache CNAME lookups
	h := in.Header()
	answers := []dns.RR{}
	m := new(dns.Msg)
	m.SetQuestion(in.Target, dns.TypeA)
	m.RecursionDesired = true
	d := new(dns.Client)
	record, _, err := d.Exchange(m, c.resolver) // TODO: try multiple resolvers
	if err != nil {
		return nil, err
	}
	if record == nil || record.Rcode == dns.RcodeNameError || record.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("Record error code %s: %s", record.Rcode, err.Error())
	}
	for _, a := range record.Answer {
		if r, ok := a.(*dns.A); ok {
			out := new(dns.A)
			out.Hdr = dns.RR_Header{Name: h.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}
			out.A = r.A
			answers = append(answers, out)
		}
	}
	return answers, nil
}

func (c *config) registerVersionHandler() { // special handler for reporting version: dig . @host TXT
	dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		if req.Question[0].Name == "." && req.Question[0].Qtype == dns.TypeTXT {
			m.Authoritative = true
			m.Answer = []dns.RR{}
			m.Answer = append(m.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: m.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: []string{"v" + version}})
			m.Extra = []dns.RR{}
			m.Extra = append(m.Extra, &dns.TXT{Hdr: dns.RR_Header{Name: m.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 0}, Txt: []string{"NedDNS"}})
		}
		w.WriteMsg(m)
	})
}

func (c *config) startServer() {
	go func() {
		srv := &dns.Server{Addr: ":" + c.port, Net: "udp"}
		err := srv.ListenAndServe()
		if err != nil {
			log.Fatalf("Failed to set udp listener %s\n", err.Error())
		}
	}()
	go func() {
		srv := &dns.Server{Addr: ":" + c.port, Net: "tcp"}
		err := srv.ListenAndServe()
		if err != nil {
			log.Fatalf("Failed to set tcp listener %s\n", err.Error())
		}
	}()
}

func parseArgs() (config, error) {
	c := config{}
	args, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		return c, err
	}
	c.lastUpdate = time.Unix(0, 0)
	c.bucket = args["<bucket>"].(string)
	c.port = args["--port"].(string)
	c.region = args["--region"].(string)
	c.debugOn = args["--debug"].(bool)
	if arg, ok := args["--resolver"].(string); ok {
		c.resolver = arg
	} else {
		c.resolver = "8.8.8.8:53"
	}
	if arg, ok := args["--log"].(string); ok {
		c.logfile = arg
	}
	if arg, ok := args["--prefix"].(string); ok {
		c.prefix = arg
	}
	c.update, err = time.ParseDuration(args["--update"].(string) + "s")
	if err != nil {
		return c, err
	}
	if arg, ok := args["--awskey"].(string); ok {
		c.awsKeyId = arg
	} else {
		c.awsKeyId = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if arg, ok := args["--awssecret"].(string); ok {
		c.awsSecret = arg
	} else {
		c.awsSecret = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if len(c.awsKeyId) < 1 || len(c.awsSecret) < 1 {
		return c, fmt.Errorf("Must use -K and -S options or set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.")
	}
	if arg, ok := args["--statsd_server"].(string); ok {
		c.statsdServer = arg
	}
	if arg, ok := args["--statsd_prefix"].(string); ok {
		c.statsdPrefix = arg
		if !strings.HasSuffix(c.statsdPrefix, ".") {
			c.statsdPrefix += "."
		}
	} else {
		c.statsdPrefix = "neddns."
	}
	return c, nil
}

func (c *config) debug(m string) {
	if c.debugOn {
		log.Println(m)
	}
}

// s3getter implements the zoneGetter interface for AWS S3
type s3getter struct {
	region string
	bucket string
	prefix string
}

func (s s3getter) ListZones() ([]zoneFile, error) {
	zones := []zoneFile{}
	connection := s3.New(&aws.Config{Region: aws.String(s.region)})
	q := s3.ListObjectsInput{
		Bucket:    aws.String(s.bucket),
		Delimiter: aws.String("/"),
		Prefix:    aws.String(s.prefix),
	}
	resp, err := connection.ListObjects(&q)
	if err != nil {
		return zones, err
	} else if resp.Contents == nil {
		return zones, fmt.Errorf("Zone directory empty")
	} else if len(resp.Contents) < 1 {
		return zones, fmt.Errorf("No zones found")
	}
	for _, k := range resp.Contents {
		zones = append(zones, zoneFile{Key: *k.Key, LastModified: *k.LastModified})
	}
	return zones, nil
}

func (s s3getter) GetZone(zoneName string) (io.ReadCloser, error) {
	connection := s3.New(&aws.Config{Region: aws.String(s.region)})
	q := s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    &zoneName,
	}
	o, err := connection.GetObject(&q)
	if err != nil {
		return nil, err
	}
	return o.Body, nil
}
