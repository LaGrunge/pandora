package main

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	"github.com/yandex/pandora/cli"
	"github.com/yandex/pandora/components/phttp"
	phttp_import "github.com/yandex/pandora/components/phttp/import"
	"github.com/yandex/pandora/core"
	coreimport "github.com/yandex/pandora/core/import"
	"github.com/yandex/pandora/core/register"
	"github.com/yandex/pandora/lib/netutil"
	"go.uber.org/zap"
)

type Gun struct {
	phttp.HTTPGun
}

func NewEpsGun(conf phttp.HTTPGunConfig) *Gun {
	return &Gun{HTTPGun: *phttp.NewHTTPGun(conf)}
}

func (b *Gun) Shoot(ammo phttp.Ammo) {
	if b.Aggregator == nil {
		zap.L().Panic("must bind before shoot")
	}
	if b.Connect != nil {
		err := b.Connect(b.Ctx)
		if err != nil {
			b.Log.Warn("Connect fail", zap.Error(err))
			return
		}
	}

	req, sample := ammo.Request()

	var requestJSON map[string]interface{}
	reader, err := req.GetBody()
	body, err := ioutil.ReadAll(reader)
	json.Unmarshal(body, &requestJSON)
	var matrixSize = len(requestJSON["targets"].([]interface{})) * len(requestJSON["sources"].([]interface{}))

	log.Println("MatrixSize", matrixSize)

	if ammo.IsInvalid() {
		sample.AddTag(phttp.EmptyTag)
		sample.SetProtoCode(0)
		b.Aggregator.Report(sample)
		b.Log.Warn("Invalid ammo", zap.Int("request", ammo.Id()))
		return
	}
	if b.DebugLog {
		b.Log.Debug("Prepared ammo to shoot", zap.Stringer("url", req.URL))
	}

	if b.Config.AutoTag.Enabled && (!b.Config.AutoTag.NoTagOnly || sample.Tags() == "") {
		sample.AddTag(autotag(b.Config.AutoTag.URIElements, req.URL))
	}
	if sample.Tags() == "" {
		sample.AddTag(phttp.EmptyTag)
	}

	defer func() {
		if err != nil {
			sample.SetErr(err)
		}
		b.Aggregator.Report(sample)
		err = errors.WithStack(err)
	}()

	var res *http.Response
	res, err = b.Do(req)
	if err != nil {
		b.Log.Warn("Request fail", zap.Error(err))
		return
	}

	sample.SetProtoCode(res.StatusCode)
	sample.SetElementsCount(matrixSize)
	defer res.Body.Close()

	_, err = io.Copy(ioutil.Discard, res.Body) // Buffers are pooled for ioutil.Discard
	if err != nil {
		b.Log.Warn("Body read fail", zap.Error(err))
		return
	}
}

func autotag(depth int, URL *url.URL) string {
	path := URL.Path
	var ind int
	for ; ind < len(path); ind++ {
		if path[ind] == '/' {
			if depth == 0 {
				break
			}
			depth--
		}
	}
	return path[:ind]
}
func endpointIsResolved(endpoint string) bool {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return false
	}
	return net.ParseIP(host) != nil
}

func preResolveTargetAddr(clientConf *phttp.ClientConfig, target *string) (err error) {
	if !clientConf.Dialer.DNSCache {
		return
	}
	if endpointIsResolved(*target) {
		clientConf.Dialer.DNSCache = false
		return
	}
	resolved, err := netutil.LookupReachable(*target)
	if err != nil {
		zap.L().Warn("DNS target pre resolve failed",
			zap.String("target", *target), zap.Error(err))
		return
	}
	clientConf.Dialer.DNSCache = false
	*target = resolved
	return
}

func main() {
	fs := coreimport.GetFs()
	coreimport.Import(fs)
	phttp_import.Import(fs)

	register.Gun("eps", func(conf phttp.HTTPGunConfig) func() core.Gun {
		preResolveTargetAddr(&conf.Client, &conf.Gun.Target)
		return func() core.Gun { return phttp.WrapGun(NewEpsGun(conf)) }
	}, phttp.DefaultHTTPGunConfig)

	cli.Run()
}
