// @author: kevinkelly
// @license: Apache Licence
// @file: main.go
// @time: 2026/10/28

// mkpkg 生成可以在 fakesr（-hosts 模式）上“安装”的假安装包，用来演示部署 / 扩容 / 升级。
// 真实的 StarRocks 安装包在假主机上无法启动：假主机通过 lib/starrocks-<fe|be|cn>.jar 里的 version=<版本> 判断节点版本。
//
//	go run ./cmd/mkpkg -version 3.5.6 -out ./pkgs        # 生成 StarRocks-3.5.6-centos-amd64.tar.gz 与 jdk-17.0.9_linux-x64_bin.tar.gz
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func write(path string, files map[string]string) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			log.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			log.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		log.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("已生成", path)
}

func main() {
	ver := flag.String("version", "3.5.6", "StarRocks 版本号（x.y.z）")
	arch := flag.String("arch", "amd64", "amd64 | arm64")
	out := flag.String("out", ".", "输出目录")
	jdk := flag.Bool("jdk", true, "同时生成一个假的 JDK 包")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	top := fmt.Sprintf("StarRocks-%s-centos-%s/", *ver, *arch)
	v := "version=" + *ver + "\n"
	write(filepath.Join(*out, fmt.Sprintf("StarRocks-%s-centos-%s.tar.gz", *ver, *arch)), map[string]string{
		top + "fe/bin/start_fe.sh": "#!/bin/bash\n", top + "fe/bin/stop_fe.sh": "#!/bin/bash\n",
		top + "fe/conf/fe.conf":         "# fe.conf\nhttp_port = 8030\nrpc_port = 9020\nquery_port = 9030\nedit_log_port = 9010\n",
		top + "fe/lib/starrocks-fe.jar": v, top + "fe/spark-dpp/spark-dpp.jar": v,
		top + "be/bin/start_be.sh": "#!/bin/bash\n", top + "be/bin/stop_be.sh": "#!/bin/bash\n",
		top + "be/bin/start_cn.sh": "#!/bin/bash\n", top + "be/bin/stop_cn.sh": "#!/bin/bash\n",
		top + "be/conf/be.conf":         "# be.conf\nbe_port = 9060\nbe_http_port = 8040\nheartbeat_service_port = 9050\nbrpc_port = 8060\n",
		top + "be/conf/cn.conf":         "# cn.conf\nbe_port = 9060\nbe_http_port = 8040\nheartbeat_service_port = 9050\nbrpc_port = 8060\n",
		top + "be/lib/starrocks-be.jar": v, top + "be/lib/starrocks-cn.jar": v,
	})
	if *jdk {
		a := "x64"
		if *arch == "arm64" {
			a = "aarch64"
		}
		write(filepath.Join(*out, fmt.Sprintf("jdk-17.0.9_linux-%s_bin.tar.gz", a)), map[string]string{"jdk-17.0.9/bin/java": "#!/bin/sh\n", "jdk-17.0.9/release": "JAVA_VERSION=17.0.9\n"})
	}
}
