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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bizflycloud/bizflyctl/formatter"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/bizflycloud/bizfly-backup/pkg/backupapi"
)

var (
	listBackupHeaders         = []string{"ID", "Name", "Path", "PolicyID", "Pattern", "Limit Upload", "Retentions", "Activated"}
	listRecoveryPointsHeaders = []string{"ID", "Name", "Status", "Type", "CREATED AT"}
	backupID                  string
	backupName                string
	recoveryPointID           string
	backupDownloadOutFile     string
)

// backupCmd represents the backup command
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Perform backup tasks.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := cmd.Help(); err != nil {
			logger.Error(err.Error())
		}
	},
}

// backupListCmd represents the backup list command
var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all current backups.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "backups"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodGet, urlRequest, nil)
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

		var c backupapi.Config
		if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}

		var data [][]string
		for _, bd := range c.BackupDirectories {
			if len(bd.Policies) == 0 {
				activated := fmt.Sprintf("%v", bd.Activated)
				row := []string{bd.ID, bd.Name, bd.Path, "", "", activated}
				data = append(data, row)
			}

			for _, policy := range bd.Policies {
				activated := fmt.Sprintf("%v", bd.Activated)
				row := []string{bd.ID, bd.Name, bd.Path, policy.ID, policy.SchedulePattern, strconv.Itoa(policy.LimitUpload), policy.Retentions, activated}
				data = append(data, row)
			}
		}

		formatter.Output(listBackupHeaders, data)
	},
}

var backupListRecoveryPointCmd = &cobra.Command{
	Use:   "list-recovery-points",
	Short: "List all recovery points of a directory.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "backups", backupID, "recovery-points"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodGet, urlRequest, nil)
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

		var rps backupapi.ListRecoveryPointsResponse
		if err := json.NewDecoder(resp.Body).Decode(&rps); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}

		data := make([][]string, 0, len(rps.RecoveryPoints))
		for _, rp := range rps.RecoveryPoints {
			data = append(data, []string{rp.ID, rp.Name, rp.Status, rp.RecoveryPointType, rp.CreatedAt})
		}

		formatter.Output(listRecoveryPointsHeaders, data)
	},
}

var backupDeleteRecoveryPointCmd = &cobra.Command{
	Use:   "delete-recovery-points",
	Short: "Delete a recovery points.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "recovery-points", recoveryPointID}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodDelete, urlRequest, nil)
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

		_, _ = io.Copy(os.Stderr, resp.Body)
	},
}

var backupDownloadRecoveryPointCmd = &cobra.Command{
	Use:   "download",
	Short: "Download backup at given recovery point.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "recovery-points", recoveryPointID, "download"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// make request
		req, err := http.NewRequest(http.MethodGet, urlRequest, nil)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		// update require header
		machineID := viper.GetString("machine_id")
		secretKey := viper.GetString("secret_key")
		if machineID == "" || secretKey == "" {
			logger.Error("The machine ID and secret key is required")
			os.Exit(1)
		}

		createdAt := time.Now().UTC().Format(http.TimeFormat)
		req.Header.Add("X-Session-Created-At", createdAt)
		req.Header.Add("X-Restore-Session-Key", restoreSessionKey(secretKey, machineID, createdAt, recoveryPointID))

		// call request
		resp, err := httpc.Do(req)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		defer resp.Body.Close()

		if backupDownloadOutFile == "" {
			backupDownloadOutFile = recoveryPointID + ".zip"
		}

		f, err := os.Create(backupDownloadOutFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		pw := backupapi.NewProgressWriter(os.Stderr)
		if _, err := io.Copy(f, io.TeeReader(resp.Body, pw)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	},
}

// backupRunCmd represents the backup run command
var backupRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a backup immediately.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "backups"}, "/")

		// create client
		httpc := http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial(tcpProtocol, strings.TrimPrefix(addr, httpPrefix))
				},
			},
		}

		// init body
		var body struct {
			ID          string `json:"id"`
			BackupName  string `json:"name"`
			StorageType string `json:"storage_type"`
		}
		body.ID = backupID
		body.BackupName = backupName
		body.StorageType = "S3"
		buf, _ := json.Marshal(body)

		// make request
		req, err := http.NewRequest(http.MethodPost, urlRequest, bytes.NewBuffer(buf))
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		// update header
		req.Header.Set("Content-Type", postContentType)

		// call request
		resp, err := httpc.Do(req)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		defer resp.Body.Close()

		_, _ = io.Copy(os.Stderr, resp.Body)
	},
}

var backupSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync backup config from server.",
	Run: func(cmd *cobra.Command, args []string) {
		// make url
		urlRequest := strings.Join([]string{addr, "backups", "sync"}, "/")

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

		// update header
		req.Header.Set("Content-Type", postContentType)

		// call request
		resp, err := httpc.Do(req)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		defer resp.Body.Close()

		_, _ = io.Copy(ioutil.Discard, resp.Body)
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)

	backupCmd.AddCommand(backupListCmd)

	backupListRecoveryPointCmd.PersistentFlags().StringVar(&backupID, "backup-id", "", "The ID of backup directory")
	_ = backupListRecoveryPointCmd.MarkPersistentFlagRequired("backup-id")

	backupDeleteRecoveryPointCmd.PersistentFlags().StringVar(&recoveryPointID, "recovery-point-id", "", "The ID of recovery point")
	_ = backupDeleteRecoveryPointCmd.MarkPersistentFlagRequired("recovery-point-id")
	backupCmd.AddCommand(backupDeleteRecoveryPointCmd)

	backupDownloadRecoveryPointCmd.PersistentFlags().StringVar(&recoveryPointID, "recovery-point-id", "", "The ID of recovery point")
	backupDownloadRecoveryPointCmd.PersistentFlags().StringVar(&backupDownloadOutFile, "outfile", "", "Output backup download to file")
	_ = backupDownloadRecoveryPointCmd.MarkPersistentFlagRequired("recovery-point-id")
	backupCmd.AddCommand(backupListRecoveryPointCmd)
	backupCmd.AddCommand(backupDownloadRecoveryPointCmd)

	backupRunCmd.PersistentFlags().StringVar(&backupID, "backup-id", "", "The ID of backup directory")
	_ = backupRunCmd.MarkPersistentFlagRequired("backup-id")
	backupRunCmd.PersistentFlags().StringVar(&backupName, "backup-name", "", "The Name of recovery point backup")
	_ = backupRunCmd.MarkPersistentFlagRequired("backup-name")
	backupCmd.AddCommand(backupRunCmd)

	backupCmd.AddCommand(backupSyncCmd)
}

func restoreSessionKey(key, machineID, createdAt, recoveryPointID string) string {
	s := strings.Join([]string{key, machineID, createdAt, recoveryPointID}, "")
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}
