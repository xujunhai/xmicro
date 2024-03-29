/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package nacos

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

import (
	"github.com/nacos-group/nacos-sdk-go/clients"
	"github.com/nacos-group/nacos-sdk-go/clients/config_client"
	nacosconst "github.com/nacos-group/nacos-sdk-go/common/constant"
	perrors "github.com/pkg/errors"
)

import (
	"xmicro/common"
	"xmicro/common/constant"
	"xmicro/config"
	"xmicro/config/parser"
	"xmicro/logger"
)

// Nacos Log dir, it can be override when creating client by config_center.log_dir
//var logDir = filepath.Join("logs", "nacos", "log")

// NacosClient Nacos client
type NacosClient struct {
	name       string
	NacosAddrs []string
	sync.Mutex // for Client
	client     *config_client.IConfigClient
	exit       chan struct{}
	Timeout    time.Duration
	once       sync.Once
	onceClose  func()
}

// GetDynamicConfiguration Get Configuration with URL
func GetNacosConfiguration(url *common.URL) (config.DynamicConfiguration, error) {
	dynamicConfiguration, err := newNacosDynamicConfiguration(url)
	if err != nil {
		return nil, err
	}
	dynamicConfiguration.SetParser(&parser.DefaultConfigurationParser{})
	return dynamicConfiguration, err
}

// Client Get Client
func (n *NacosClient) Client() *config_client.IConfigClient {
	return n.client
}

// SetClient Set client
func (n *NacosClient) SetClient(client *config_client.IConfigClient) {
	n.Lock()
	n.client = client
	n.Unlock()
}

type option func(*options)

type options struct {
	nacosName string
	client    *NacosClient
}

// WithNacosName Set nacos name
func WithNacosName(name string) option {
	return func(opt *options) {
		opt.nacosName = name
	}
}

// ValidateNacosClient Validate nacos client , if null then create it
func ValidateNacosClient(container nacosClientFacade, opts ...option) error {
	if container == nil {
		return perrors.Errorf("container can not be null")
	}
	os := &options{}
	for _, opt := range opts {
		opt(os)
	}

	url := container.GetUrl()
	timeout, err := time.ParseDuration(url.GetParam(constant.RegistryTimeoutKey, constant.DefaultRegTimeout))
	if err != nil {
		logger.Errorf("invalid timeout config %+v,got err %+v",
			url.GetParam(constant.RegistryTimeoutKey, constant.DefaultRegTimeout), err)
		return perrors.WithMessagef(err, "newNacosClient(address:%+v)", url.Location)
	}
	nacosAddresses := strings.Split(url.Location, ",")
	if container.NacosClient() == nil {
		//in dubbo ,every registry only connect one node ,so this is []string{r.Address}
		newClient, err := newNacosClient(os.nacosName, nacosAddresses, timeout, url)
		if err != nil {
			logger.Errorf("newNacosClient(name{%s}, nacos address{%v}, timeout{%d}) = error{%v}",
				os.nacosName, url.Location, timeout.String(), err)
			return perrors.WithMessagef(err, "newNacosClient(address:%+v)", url.Location)
		}
		container.SetNacosClient(newClient)
	}

	if container.NacosClient().Client() == nil {
		configClient, err := initNacosConfigClient(nacosAddresses, timeout, url)
		if err != nil {
			logger.Errorf("initNacosConfigClient(addr:%+v,timeout:%v,url:%v) = err %+v",
				nacosAddresses, timeout.String(), url, err)
			return perrors.WithMessagef(err, "newNacosClient(address:%+v)", url.Location)
		}
		container.NacosClient().SetClient(&configClient)

	}

	return perrors.WithMessagef(nil, "newNacosClient(address:%+v)", url.PrimitiveURL)
}

func newNacosClient(name string, nacosAddrs []string, timeout time.Duration, url *common.URL) (*NacosClient, error) {
	var (
		err error
		n   *NacosClient
	)

	n = &NacosClient{
		name:       name,
		NacosAddrs: nacosAddrs,
		Timeout:    timeout,
		exit:       make(chan struct{}),
		onceClose: func() {
			close(n.exit)
		},
	}

	configClient, err := initNacosConfigClient(nacosAddrs, timeout, url)
	if err != nil {
		logger.Errorf("initNacosConfigClient(addr:%+v,timeout:%v,url:%v) = err %+v",
			nacosAddrs, timeout.String(), url, err)
		return n, perrors.WithMessagef(err, "newNacosClient(address:%+v)", url.Location)
	}
	n.SetClient(&configClient)

	return n, nil
}

func initNacosConfigClient(nacosAddrs []string, timeout time.Duration, url *common.URL) (config_client.IConfigClient, error) {
	var svrConfList []nacosconst.ServerConfig
	for _, nacosAddr := range nacosAddrs {
		split := strings.Split(nacosAddr, ":")
		port, err := strconv.ParseUint(split[1], 10, 64)
		if err != nil {
			logger.Errorf("strconv.ParseUint(nacos addr port:%+v) = error %+v", split[1], err)
			continue
		}
		svrconf := nacosconst.ServerConfig{
			IpAddr: split[0],
			Port:   port,
		}
		svrConfList = append(svrConfList, svrconf)
	}

	return clients.CreateConfigClient(map[string]interface{}{
		"serverConfigs": svrConfList,
		"clientConfig": nacosconst.ClientConfig{
			TimeoutMs:           uint64(int32(timeout / time.Millisecond)),
			ListenInterval:      uint64(int32(timeout / time.Millisecond)),
			NotLoadCacheAtStart: true,
			LogDir:              url.GetParam(constant.NacosLogDirKey, ""),
			CacheDir:            url.GetParam(constant.NacosCacheDirKey, ""),
			Endpoint:            url.GetParam(constant.NacosEndpoint, ""),
			Username:            url.GetParam(constant.NacosUsername, ""),
			Password:            url.GetParam(constant.NacosPassword, ""),
			NamespaceId:         url.GetParam(constant.NacosNamespaceId, ""),
		},
	})
}

// Done Get nacos client exit signal
func (n *NacosClient) Done() <-chan struct{} {
	return n.exit
}

func (n *NacosClient) stop() bool {
	select {
	case <-n.exit:
		return true
	default:
		n.once.Do(n.onceClose)
	}

	return false
}

// NacosClientValid Get nacos client valid status
func (n *NacosClient) NacosClientValid() bool {
	select {
	case <-n.exit:
		return false
	default:
	}

	valid := true
	n.Lock()
	if n.Client() == nil {
		valid = false
	}
	n.Unlock()

	return valid
}

// Close Close nacos client , then set null
func (n *NacosClient) Close() {
	if n == nil {
		return
	}

	n.stop()
	n.SetClient(nil)
}
