// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-Present Datadog, Inc.

package collectors

import (
	"fmt"

	"github.com/DataDog/datadog-agent/pkg/util/cloudfoundry"
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
	infoOut    chan<- []*TagInfo
	gardenUtil *cloudfoundry.GardenUtil
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
	}

	c.infoOut = out
	return PullCollection, nil
}

// Pull gets the list of containers
func (c *GardenCollector) Pull() error {
	containerList, err := c.gardenUtil.GetGardenContainers()
	if err != nil {
		return err
	}

	var tagInfo = make([]*TagInfo, len(containerList))
	for i, c := range containerList {
		containerHandle := containers.BuildTaggerEntityName(c.Handle())
		tagInfo[i] = &TagInfo{
			Source:       gardenCollectorName,
			Entity:       containerHandle,
			HighCardTags: []string{fmt.Sprintf("container_name:%s", containerHandle)},
		}
	}
	c.infoOut <- tagInfo
	return nil
}

// Fetch gets the tags for a specific entity
func (c *GardenCollector) Fetch(entity string) ([]string, []string, []string, error) {
	return []string{}, []string{}, []string{fmt.Sprintf("container_name:%s", entity)}, nil
}

func gardenFactory() Collector {
	return &GardenCollector{}
}

func init() {
	registerCollector(gardenCollectorName, gardenFactory, NodeRuntime)
}
