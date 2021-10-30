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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bizflycloud/bizfly-backup/pkg/cache"
	"github.com/spf13/cobra"
)

const (
	cacheDir = ".cache"
)

var cleanupCacheCmd = &cobra.Command{
	Use:   "cleanup-cache [max number of hours (integer) the old cache to be cleanup]",
	Short: "Remove old cache directories",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		number, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}
		maxCacheAge := time.Duration(number) * time.Hour

		oldCacheDirs, err := cache.Old(cacheDir, maxCacheAge)
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}
		fmt.Printf("%d old cache dirs found \n", len(oldCacheDirs))

		if len(oldCacheDirs) != 0 {
			for _, item := range oldCacheDirs {
				dir := filepath.Join(cacheDir, item.Name())
				err = os.RemoveAll(dir)
				if err != nil {
					logger.Error(err.Error())
					os.Exit(1)
				}
				fmt.Printf("removing old cache dirs %s \n", dir)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(cleanupCacheCmd)
}
