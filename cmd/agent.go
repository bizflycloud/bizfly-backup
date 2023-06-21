// This file is part of bizfly-backup
//
// Copyright (C) 2020  BizFly Cloud
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
	"github.com/bizflycloud/bizfly-backup/pkg/broker/mqtt"
	"github.com/bizflycloud/bizfly-backup/pkg/server"
)

// agentCmd represents the agent command
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run agent.",
	Run: func(cmd *cobra.Command, args []string) {
		// create logger
		logger, err := backupapi.WriteLog()
		if err != nil {
			panic(err)
		}

		machineID := viper.GetString("machine_id")
		accessKey := viper.GetString("access_key")
		secretKey := viper.GetString("secret_key")
		apiUrl := viper.GetString("api_url")
		numGoroutine := viper.GetInt("num_goroutine")
		Host := viper.GetString("db_host")
		Port, _ := strconv.Atoi(viper.GetString("db_port"))
		Database := viper.GetString("db_database")
		Username := viper.GetString("db_username")
		Password := viper.GetString("db_password")

		dataBase := backupapi.Database{
			Host:     Host,
			Port:     Port,
			Database: Database,
			Username: Username,
			Password: Password,
		}

		backupClient, err := backupapi.NewClient(
			backupapi.WithAccessKey(accessKey),
			backupapi.WithSecretKey(secretKey),
			backupapi.WithServerURL(apiUrl),
			backupapi.WithID(machineID),
			backupapi.WithNumGoroutine(numGoroutine),
			backupapi.WithDatabase(&dataBase),
		)
		if err != nil {
			logger.Error("failed to create new backup client", zap.Error(err))
			os.Exit(1)
		}
		bo := backoff.WithMaxRetries(backoff.NewConstantBackOff(3*time.Second), 3)
		var brokerUrl string
		for {
			umr, err := backupClient.UpdateMachine()
			if err == nil {
				brokerUrl = umr.BrokerUrl
				numGoroutine = umr.NumGoroutine
				break
			}
			logger.Error("failed to update machine info", zap.Error(err))
			d := bo.NextBackOff()
			if d == backoff.Stop {
				os.Exit(1)
			}
			time.Sleep(d)
		}

		mqttUrl := brokerUrl
		fmt.Println(mqttUrl)
		agentID := machineID
		b, err := mqtt.NewBroker(
			mqtt.WithURL(mqttUrl),
			mqtt.WithClientID(agentID),
			mqtt.WithUsername(accessKey),
			mqtt.WithPassword(secretKey),
			mqtt.WithLogger(logger),
		)
		if err != nil {
			logger.Fatal("failed to create broker", zap.Error(err))
			os.Exit(1)
		}

		logger.Debug("Listening address: " + addr)
		s, err := server.New(
			server.WithAddr(addr),
			server.WithBroker(b),
			server.WithSubscribeTopics("agent/default", "agent/"+agentID),
			server.WithPublishTopics("agent/"+agentID, "agent/recovery-points/"+agentID),
			server.WithBackupClient(backupClient),
			server.WithLogger(logger),
			server.WithNumGoroutine(numGoroutine),
		)
		if err != nil {
			logger.Fatal("failed to create new server", zap.Error(err))
			os.Exit(1)
		}
		if err := s.Run(); !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server run failed", zap.Error(err))
			os.Exit(1)
		}
	},
}

var agentVersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version of agent server.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "version"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodPost, urlRequest, nil)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		// call request
		resp, err := httpc.Do(req)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		defer resp.Body.Close()

		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		fmt.Println(string(b))
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentVersionCmd)
}
