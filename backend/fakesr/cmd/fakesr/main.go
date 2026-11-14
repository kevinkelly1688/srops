// @author: kevinkelly
// @license: Apache Licence
// @file: main.go
// @time: 2026/10/28

// fakesr 是一个用于本地开发/测试的假 StarRocks 集群：
// MySQL 协议查询端口、每个节点的 HTTP /metrics 以及一个 SSH 服务。
package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"starrocks-ops/fakesr"
)

func main() {
	d := fakesr.DefaultConfig()
	cfg := d
	flag.StringVar(&cfg.Host, "host", d.Host, "listen / advertised host")
	flag.IntVar(&cfg.QueryPort, "query-port", d.QueryPort, "MySQL protocol port")
	flag.StringVar(&cfg.User, "user", d.User, "MySQL user")
	flag.StringVar(&cfg.Password, "password", d.Password, "MySQL password (also SSH password for user starrocks)")
	flag.StringVar(&cfg.Mode, "mode", d.Mode, "shared-nothing | shared-data")
	flag.StringVar(&cfg.Scenario, "scenario", d.Scenario, "healthy | problems")
	flag.IntVar(&cfg.SSHPort, "ssh-port", d.SSHPort, "SSH port (0 disables)")
	flag.StringVar(&cfg.Version, "version", d.Version, "StarRocks version (shared-data defaults to 4.0.2)")
	flag.BoolVar(&cfg.Hosts, "hosts", false, "give every node its own loopback address (127.0.0.11+) and SSH host; needs a fixed -ssh-port")
	flag.IntVar(&cfg.Spare, "spare", 0, "with -hosts: extra blank hosts (127.0.0.50+) for deploy / scale-out")
	flag.StringVar(&cfg.FEDir, "fe-dir", d.FEDir, "with -hosts: FE install dir")
	flag.StringVar(&cfg.BEDir, "be-dir", d.BEDir, "with -hosts: BE / CN install dir")
	s3Port := flag.Int("s3-port", -1, "start an in-memory S3 service for the backup repositories (0 = random port, -1 = off); access key id: -s3-access-key")
	flag.StringVar(&cfg.S3AccessKey, "s3-access-key", "fakeak", "access key id accepted by the S3 service (any secret)")
	verbose := flag.Bool("v", false, "log every query / ssh command")
	flag.Parse()
	if *s3Port >= 0 {
		cfg.S3, cfg.S3Port = true, *s3Port
	}

	versionSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "version" {
			versionSet = true
		}
	})
	if !versionSet && cfg.Mode == fakesr.ModeSharedData {
		cfg.Version = "4.0.2"
	}
	if cfg.SSHPort == 0 {
		cfg.SSHPort = -1
	}

	logger := log.New(os.Stderr, "fakesr ", log.LstdFlags)
	if *verbose {
		cfg.Logger = logger
	}

	c, err := fakesr.Start(cfg)
	if err != nil {
		logger.Fatalf("start: %v", err)
	}
	logger.Printf("mode=%s scenario=%s version=%s", cfg.Mode, cfg.Scenario, c.VersionString())
	logger.Printf("mysql: %s (user %s)", c.QueryAddr(), cfg.User)
	logger.Printf("fe http: %v", c.FEHTTPAddrs())
	if c.HasBE() {
		logger.Printf("be http: %v", c.BEHTTPAddrs())
	}
	logger.Printf("cn http: %v", c.CNHTTPAddrs())
	if a := c.SSHAddr(); a != "" {
		logger.Printf("ssh: %s (user starrocks)", a)
	}
	if a := c.S3Addr(); a != "" {
		logger.Printf("s3: http://%s (path-style, access key %s; repo_s3_prod = s3a://sr-backup/prod-sr-01)", a, cfg.S3AccessKey)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logger.Printf("shutting down")
	c.Stop()
}
