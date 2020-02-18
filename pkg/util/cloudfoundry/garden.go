// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-Present Datadog, Inc.

package cloudfoundry

import (
	"fmt"
	"net"
	"sync"
	"time"

	"code.cloudfoundry.org/garden"
	"code.cloudfoundry.org/garden/client"
	"code.cloudfoundry.org/garden/client/connection"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/containers/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/retry"
)

var (
	globalGardenUtil     *GardenUtil
	globalGardenUtilLock sync.Mutex
)

// GardenUtil wraps interactions with a local garden API.
type GardenUtil struct {
	retrier retry.Retrier
	cli     client.Client
}

func GetGardenUtil() (*GardenUtil, error) {
	globalGardenUtilLock.Lock()
	defer globalGardenUtilLock.Unlock()
	network := "unix"
	address := "/var/vcap/data/garden/garden.sock"
	if globalGardenUtil == nil {
		globalGardenUtil = &GardenUtil{
			cli: client.New(connection.New(network, address)),
		}
		globalGardenUtil.retrier.SetupRetrier(&retry.Config{
			Name:          "gardenUtil",
			AttemptMethod: globalGardenUtil.cli.Ping,
			Strategy:      retry.RetryCount,
			RetryCount:    10,
			RetryDelay:    30 * time.Second,
		})
	}
	if err := globalGardenUtil.retrier.TriggerRetry(); err != nil {
		log.Debugf("could not initiate connection to garden server %s using network %s: %s", address, network, err)
		return nil, err
	}
	return globalGardenUtil, nil
}
func (gu *GardenUtil) GetGardenContainers() ([]garden.Container, error) {
	return gu.cli.Containers(nil)
}

func (gu *GardenUtil) ListContainers() ([]*containers.Container, error) {
	gardenContainers, err := gu.GetGardenContainers()
	if err != nil {
		return nil, fmt.Errorf("error listing garden containers: %v", err)
	}

	var cList = make([]*containers.Container, len(gardenContainers))
	handles := make([]string, len(gardenContainers))
	for i, gardenContainer := range gardenContainers {
		handles[i] = gardenContainer.Handle()
	}
	gardenContainerInfo, err := gu.cli.BulkInfo(handles)
	if err != nil {
		return nil, fmt.Errorf("error getting info for garden containers: %v", err)
	}
	gardenContainerMetrics, err := gu.cli.BulkMetrics(handles)
	if err != nil {
		return nil, fmt.Errorf("error getting info for garden containers: %v", err)
	}

	for i, handle := range handles {
		infoEntry := gardenContainerInfo[handle]
		if err := infoEntry.Err; err != nil {
			log.Debugf("could not get info for container %s: %v", handle, err)
			continue
		}
		metricsEntry := gardenContainerMetrics[handle]
		if err := metricsEntry.Err; err != nil {
			log.Debugf("could not get info for container %s: %v", handle, err)
			continue
		}
		container := containers.Container{
			Type:        "garden",
			ID:          handle,
			EntityID:    containers.BuildTaggerEntityName(handle),
			State:       infoEntry.Info.State,
			Excluded:    false,
			Created:     time.Now().Add(-metricsEntry.Metrics.Age).Unix(),
			AddressList: parseContainerPorts(infoEntry.Info),
		}
		cList[i] = &container
	}

	cgByContainer, err := metrics.ScrapeAllCgroups()
	if err != nil {
		return nil, fmt.Errorf("could not get cgroups: %s", err)
	}
	for _, container := range cList {
		if container.State != containers.ContainerActiveState {
			continue
		}
		cgroup, ok := cgByContainer[container.ID]
		if !ok {
			log.Debugf("No matching cgroups for container %s, skipping", container.ID[:12])
			continue
		}
		container.SetCgroups(cgroup)
		err = container.FillCgroupLimits()
		if err != nil {
			log.Debugf("Cannot get limits for container %s: %s, skipping", container.ID[:12], err)
			continue
		}
	}
	err = gu.UpdateContainerMetrics(cList)
	return cList, err
}

func (gu *GardenUtil) UpdateContainerMetrics(cList []*containers.Container) error {
	for _, container := range cList {
		if container.State != containers.ContainerActiveState {
			continue
		}

		err := container.FillCgroupMetrics()
		if err != nil {
			log.Debugf("Cannot get metrics for container %s: %s", container.ID[:12], err)
			continue
		}
		err = container.FillNetworkMetrics(map[string]string{})
		if err != nil {
			log.Debugf("Cannot get network metrics for container %s: %s", container.ID[:12], err)
			continue
		}
	}
	return nil
}

func parseContainerPorts(info garden.ContainerInfo) []containers.NetworkAddress {
	var addresses = make([]containers.NetworkAddress, len(info.MappedPorts))
	for i, port := range info.MappedPorts {
		addresses[i] = containers.NetworkAddress{
			IP:       net.ParseIP(info.ExternalIP),
			Port:     int(port.HostPort),
			Protocol: "tcp",
		}
	}
	return addresses
}
