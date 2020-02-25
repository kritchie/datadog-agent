// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-Present Datadog, Inc.

package collectors

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/util/cloudfoundry"
	"github.com/DataDog/datadog-agent/pkg/util/clusteragent"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/retry"
)

const (
	gardenCollectorName = "cloudfoundry"
)

// GardenCollector listen to the ECS agent to get ECS metadata.
// Relies on the DockerCollector to trigger deletions, it's not intended to run standalone
type GardenCollector struct {
	infoOut             chan<- []*TagInfo
	gardenUtil          *cloudfoundry.GardenUtil
	dcaClient           clusteragent.DCAClientInterface
	bbsCache            *cloudfoundry.BBSCache
	clusterAgentEnabled bool
}

// Detect tries to connect to the Garden API
func (c *GardenCollector) Detect(out chan<- []*TagInfo) (CollectionMode, error) {
	var err error
	c.gardenUtil, err = cloudfoundry.GetGardenUtil()
	if err != nil {
		if retry.IsErrWillRetry(err) {
			log.Errorf("Could not connect to the local garden server: %v", err)
			return NoCollection, err
		}
		log.Errorf("Permanent failure trying to connect with the local garden server")
		return NoCollection, err
	}

	// if DCA is enabled and can't communicate with the DCA, let the tagger retry.
	var errDCA error
	log.Errorf("cluster agent detect", config.Datadog.Get("tags"))
	if config.Datadog.GetBool("cluster_agent.enabled") {
		log.Errorf("Cluster agent enabled")
		c.clusterAgentEnabled = false
		c.dcaClient, errDCA = clusteragent.GetClusterAgentClient()
		if errDCA != nil {
			log.Errorf("Could not initialise the communication with the cluster agent: %s", errDCA.Error())
			// continue to retry while we can
			if retry.IsErrWillRetry(errDCA) {
				return NoCollection, errDCA
			}
			// we return the permanent fail only if fallback is disabled
			if retry.IsErrPermaFail(errDCA) && !config.Datadog.GetBool("cluster_agent.tagging_fallback") {
				return NoCollection, errDCA
			}
			log.Errorf("Permanent failure in communication with the cluster agent, will fallback to Diego BBS API")
		} else {
			log.Errorf("Cluster agent ok")
			c.clusterAgentEnabled = true
		}
	}

	if !config.Datadog.GetBool("cluster_agent.enabled") || errDCA != nil {
		pollInterval := time.Second * time.Duration(config.Datadog.GetInt("cloud_foundry_bbs.poll_interval"))
		// NOTE: we can't use GetPollInterval in ConfigureGlobalBBSCache, as that causes import cycle
		bc, err := cloudfoundry.ConfigureGlobalBBSCache(
			config.Datadog.GetString("cloud_foundry_bbs.url"),
			config.Datadog.GetString("cloud_foundry_bbs.ca_file"),
			config.Datadog.GetString("cloud_foundry_bbs.cert_file"),
			config.Datadog.GetString("cloud_foundry_bbs.key_file"),
			pollInterval,
			false,
		)
		if err != nil {
			return NoCollection, fmt.Errorf("failed to initialize BBS Cache: %s", err.Error())
		}
		c.bbsCache = bc
	}

	c.infoOut = out
	return PullCollection, nil
}

// Pull gets the list of containers
func (c *GardenCollector) Pull() error {
	tagInfo := []*TagInfo{}
	tagsByInstanceGUID, err := c.dcaClient.GetAllCFAppsMetadata()
	if err != nil {
		return err
	}
	for handle, tags := range tagsByInstanceGUID {
		entity := containers.BuildTaggerEntityName(handle)
		tagInfo = append(tagInfo, &TagInfo{
			Source:       gardenCollectorName,
			Entity:       entity,
			HighCardTags: tags,
		})
	}
	c.infoOut <- tagInfo
	return nil
}

// Fetch gets the tags for a specific entity
func (c *GardenCollector) Fetch(entity string) ([]string, []string, []string, error) {
	tagsByInstanceGUID, err := c.dcaClient.GetAllCFAppsMetadata()
	if err != nil {
		return []string{}, []string{}, []string{}, err
	}
	_, cid := containers.SplitEntityName(entity)
	return []string{}, []string{}, tagsByInstanceGUID[cid], nil
}

func gardenFactory() Collector {
	return &GardenCollector{}
}

func init() {
	registerCollector(gardenCollectorName, gardenFactory, NodeRuntime)
}
