package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

const (
	DefaultConfigs = "doh.json;~/.doh.json;/etc/doh.json"
)

var (
	ErrConfigParse = errors.New("config parse error")
	ErrParameter   = errors.New("parameter error")
	FmtShort       bool
	FmtJson        bool
	QType          string
	Subnet         string
	Driver         string
	URL            string
	Insecure       bool
	Aliases        *map[string]string
)

type DriverHeader struct {
	Driver string
	URL    string
}

func (header *DriverHeader) CreateClient(body json.RawMessage) (cli Client, err error) {
	if URL, ok := (*Aliases)[header.URL]; ok {
		header.URL = URL
	}

	if header.Driver == "" {
		header.Driver, err = GuessDriver(header.URL)
		if err != nil {
			logger.Error(err.Error())
			return
		}
	}

	switch header.Driver {
	case "dns":
		cli, err = NewDnsClient(header.URL)
	case "google":
		cli, err = NewGoogleClient(header.URL, body)
	case "rfc8484":
		cli, err = NewRfc8484Client(header.URL, body)
	default:
		err = ErrConfigParse
	}

	return
}

func (header *DriverHeader) CreateService(cli Client, body json.RawMessage) (srv Server, err error) {
	if header.Driver == "" {
		header.Driver, err = GuessDriver(header.URL)
		if err != nil {
			logger.Error(err.Error())
			return
		}
	}

	switch header.Driver {
	case "dns":
		srv, err = NewDnsServer(cli, header.URL, body)
	case "doh", "http", "https":
		srv, err = NewDoHServer(cli, header.URL, body)
	default:
		err = ErrConfigParse
		return
	}

	if err != nil {
		logger.Error(err.Error())
		return
	}
	return
}

type Config struct {
	Logfile  string
	Loglevel string
	Service  json.RawMessage
	Client   json.RawMessage
	Aliases  map[string]string
}

func (cfg *Config) CreateClient() (cli Client, err error) {
	var header DriverHeader
	if cfg.Client != nil {
		err = json.Unmarshal(cfg.Client, &header)
		if err != nil {
			logger.Error(err.Error())
			return
		}
	}

	if URL != "" {
		header.URL = URL
	}

	if Driver != "" {
		header.Driver = Driver
	}

	cli, err = header.CreateClient(cfg.Client)
	if err != nil {
		logger.Error(err.Error())
		return
	}

	return
}

func (cfg *Config) CreateService(cli Client) (srv Server, err error) {
	var header DriverHeader
	if cfg.Service != nil {
		err = json.Unmarshal(cfg.Service, &header)
		if err != nil {
			logger.Error(err.Error())
			return
		}
	}

	srv, err = header.CreateService(cli, cfg.Service)
	if err != nil {
		logger.Error(err.Error())
		return
	}

	return
}

func QueryDN(cli Client, dn string) (err error) {
	qtype, ok := dns.StringToType[QType]
	if !ok {
		err = ErrParameter
		return
	}

	ctx := context.Background()
	quiz := &dns.Msg{}
	quiz.SetQuestion(dns.Fqdn(dn), qtype)

	if Subnet != "" {
		var addr net.IP
		var mask uint8
		addr, mask, err = ParseSubnet(Subnet)
		if err != nil {
			logger.Error(err.Error())
			return
		}
		appendEdns0Subnet(quiz, addr, mask)
	}

	start := time.Now()

	ans, err := cli.Exchange(ctx, quiz)
	if err != nil {
		logger.Error(err.Error())
		return
	}

	elapsed := time.Since(start)

	switch {
	case FmtShort:
		for _, rr := range ans.Answer {
			switch v := rr.(type) {
			case *dns.A:
				fmt.Println(v.A.String())
			case *dns.AAAA:
				fmt.Println(v.AAAA.String())
			case *dns.CNAME:
				fmt.Println(v.Target)
			}
		}

	case FmtJson:
		jsonresp := &DNSMsg{}
		err = jsonresp.FromAnswer(quiz, ans)
		if err != nil {
			logger.Error(err.Error())
			return
		}

		var bresp []byte
		bresp, err = json.Marshal(jsonresp)
		if err != nil {
			logger.Error(err.Error())
			return
		}

		fmt.Printf("%s", string(bresp))

	default:
		fmt.Println(ans.String())
		fmt.Printf(";; Query time: %d msec\n", elapsed.Milliseconds())
		fmt.Printf(";; SERVER: %s\n", cli.Url())
		fmt.Printf(";; WHEN: %s\n\n", start.Format(time.UnixDate))
	}

	return
}

func main() {
	var err error
	var ConfigFile string
	var Loglevel string
	var Profile string
	var Query bool
	flag.StringVar(&ConfigFile, "config", "", "config file")
	flag.StringVar(&Loglevel, "loglevel", "", "log level")
	flag.StringVar(&Profile, "profile", "", "run profile")
	flag.BoolVar(&Query, "q", false, "query")
	flag.BoolVar(&FmtShort, "short", false, "show short answer")
	flag.BoolVar(&FmtJson, "json", false, "show json answer")
	flag.StringVar(&Subnet, "subnet", "", "edns client subnet")
	flag.StringVar(&QType, "type", "A", "qtype")
	flag.StringVar(&Driver, "driver", "", "client driver")
	flag.StringVar(&URL, "url", "", "client url")
	flag.BoolVar(&Insecure, "insecure", false, "don't check cert in https")
	flag.Parse()

	cfg := &Config{}
	err = LoadJson(DefaultConfigs, cfg)
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	if ConfigFile != "" {
		err = LoadJson(ConfigFile, cfg)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
	}

	if Loglevel != "" {
		cfg.Loglevel = Loglevel
	}
	SetLogging(cfg.Logfile, cfg.Loglevel)

	Aliases = &cfg.Aliases

	cli, err := cfg.CreateClient()
	if err != nil {
		logger.Error(err.Error())
		return
	}

	logger.Debugf("%+v", cli)

	switch {
	case Query:
		for _, dn := range flag.Args() {
			QueryDN(cli, dn)
		}

	case cfg.Service != nil:
		if Profile != "" {
			go func() {
				logger.Infof("golang profile %s", Profile)
				logger.Infof("golang profile result: %s",
					http.ListenAndServe(Profile, nil))
			}()
		}

		var srv Server
		srv, err = cfg.CreateService(cli)
		if err != nil {
			logger.Error(err.Error())
			return
		}

		err = srv.Run()
		if err != nil {
			logger.Error(err.Error())
			return
		}

	default:
		logger.Error("no query nor server, quit.")
		return
	}

	return
}
