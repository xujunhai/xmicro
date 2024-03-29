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
	"xmicro/common/component"
	"strings"
	"sync"
)

import (
	gxset "github.com/dubbogo/gost/container/set"
	"github.com/nacos-group/nacos-sdk-go/vo"
	perrors "github.com/pkg/errors"
)

import (
	"xmicro/common"
	"xmicro/common/constant"
	"xmicro/config"
	"xmicro/config/parser"
	"xmicro/logger"
)

const (
	nacosClientName = "nacos config"
	// the number is a little big tricky
	// it will be used in query which looks up all keys with the target group
	// now, one key represents one application
	// so only a group has more than 9999 applications will failed
	maxKeysNum = 9999
)

// nacosDynamicConfiguration is the implementation of DynamicConfiguration based on nacos
type nacosDynamicConfiguration struct {
	url          *common.URL
	rootPath     string
	wg           sync.WaitGroup
	cltLock      sync.Mutex
	done         chan struct{}
	client       *NacosClient
	keyListeners sync.Map
	parser       parser.ConfigurationParser
}

func init() {
	component.SetConfigCenterFactory(constant.NacosKey, &nacosDynamicConfigurationFactory{})
}

type nacosDynamicConfigurationFactory struct {
}

// GetDynamicConfiguration Get Configuration with URL
func (f *nacosDynamicConfigurationFactory) GetDynamicConfiguration(url *common.URL) (config.DynamicConfiguration, error) {
	return GetNacosConfiguration(url)
}

func newNacosDynamicConfiguration(url *common.URL) (*nacosDynamicConfiguration, error) {
	c := &nacosDynamicConfiguration{
		rootPath: "/" + url.GetParam(constant.ConfigNamespaceKey, config.DefaultGroup) + "/config",
		url:      url,
		done:     make(chan struct{}),
	}
	err := ValidateNacosClient(c, WithNacosName(nacosClientName))
	if err != nil {
		logger.Errorf("nacos client start error ,error message is %v", err)
		return nil, err
	}
	c.wg.Add(1)
	go HandleClientRestart(c)
	return c, err

}

// AddListener Add listener
func (n *nacosDynamicConfiguration) AddListener(key string, listener config.ConfigurationListener, opions ...config.Option) {
	n.addListener(key, listener)
}

// RemoveListener Remove listener
func (n *nacosDynamicConfiguration) RemoveListener(key string, listener config.ConfigurationListener, opions ...config.Option) {
	n.removeListener(key, listener)
}

// GetProperties nacos distinguishes configuration files based on group and dataId. defalut group = "micro" and dataId = key
func (n *nacosDynamicConfiguration) GetProperties(key string, opts ...config.Option) (string, error) {
	return n.GetRule(key, opts...)
}

// GetInternalProperty Get properties value by key
func (n *nacosDynamicConfiguration) GetInternalProperty(key string, opts ...config.Option) (string, error) {
	return n.GetProperties(key, opts...)
}

// PublishConfig will publish the config with the (key, group, value) pair
func (n *nacosDynamicConfiguration) PublishConfig(key string, group string, value string) error {

	group = n.resolvedGroup(group)

	ok, err := (*n.client.Client()).PublishConfig(vo.ConfigParam{
		DataId:  key,
		Group:   group,
		Content: value,
	})

	if err != nil {
		return perrors.WithStack(err)
	}
	if !ok {
		return perrors.New("publish config to Nocos failed")
	}
	return nil
}

// GetConfigKeysByGroup will return all keys with the group
func (n *nacosDynamicConfiguration) GetConfigKeysByGroup(group string) (*gxset.HashSet, error) {
	group = n.resolvedGroup(group)
	page, err := (*n.client.Client()).SearchConfig(vo.SearchConfigParm{
		Search: "accurate",
		Group:  group,
		PageNo: 1,
		// actually it's impossible for user to create 9999 application under one group
		PageSize: maxKeysNum,
	})

	result := gxset.NewSet()
	if err != nil {
		return result, perrors.WithMessage(err, "can not find the client config")
	}
	for _, itm := range page.PageItems {
		result.Add(itm.DataId)
	}
	return result, nil
}

// GetRule Get router rule
func (n *nacosDynamicConfiguration) GetRule(key string, opts ...config.Option) (string, error) {
	tmpOpts := &config.Options{}
	for _, opt := range opts {
		opt(tmpOpts)
	}
	content, err := (*n.client.Client()).GetConfig(vo.ConfigParam{
		DataId: key,
		Group:  n.resolvedGroup(tmpOpts.Group),
	})
	if err != nil {
		return "", perrors.WithStack(err)
	} else {
		return content, nil
	}
}

// Parser Get Parser
func (n *nacosDynamicConfiguration) Parser() parser.ConfigurationParser {
	return n.parser
}

// SetParser Set Parser
func (n *nacosDynamicConfiguration) SetParser(p parser.ConfigurationParser) {
	n.parser = p
}

// NacosClient Get Nacos Client
func (n *nacosDynamicConfiguration) NacosClient() *NacosClient {
	return n.client
}

// SetNacosClient Set Nacos Client
func (n *nacosDynamicConfiguration) SetNacosClient(client *NacosClient) {
	n.cltLock.Lock()
	n.client = client
	n.cltLock.Unlock()
}

// WaitGroup for wait group control, zk client listener & zk client container
func (n *nacosDynamicConfiguration) WaitGroup() *sync.WaitGroup {
	return &n.wg
}

// GetDone For nacos client control	RestartCallBack() bool
func (n *nacosDynamicConfiguration) GetDone() chan struct{} {
	return n.done
}

// GetUrl Get Url
func (n *nacosDynamicConfiguration) GetUrl() *common.URL {
	return n.url
}

// Destroy Destroy configuration instance
func (n *nacosDynamicConfiguration) Destroy() {
	close(n.done)
	n.wg.Wait()
	n.closeConfigs()
}

// resolvedGroup will regular the group. Now, it will replace the '/' with '-'.
// '/' is a special character for nacos
func (n *nacosDynamicConfiguration) resolvedGroup(group string) string {
	if len(group) <= 0 {
		return group
	}
	return strings.ReplaceAll(group, "/", "-")
}

// IsAvailable Get available status
func (n *nacosDynamicConfiguration) IsAvailable() bool {
	select {
	case <-n.done:
		return false
	default:
		return true
	}
}

func (n *nacosDynamicConfiguration) closeConfigs() {
	n.cltLock.Lock()
	client := n.client
	n.client = nil
	n.cltLock.Unlock()
	// Close the old client first to close the tmp node
	client.Close()
	logger.Infof("begin to close provider n client")
}

//TODO RemoveConfig will remove the config white the (key, group) pair
func (n *nacosDynamicConfiguration) RemoveConfig(string, string) error {
	return nil
}
