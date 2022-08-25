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
	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"os"
	"strings"
)

const (
	defaultPort = 9000
	httpPrefix  = "http://"
	localhost   = "127.0.0.1"
	tcpProtocol = "tcp"
)

var (
	cfgFile string
	addr    string
	debug   bool
	force   bool
	logger  *zap.Logger
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "bizfly-backup",
	Short: "BizFly Cloud backup agent.",
	Long:  `BizFly Cloud backup agent is a CLI application to interact with BizFly Cloud Backup Service.`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := cmd.Help(); err != nil {
			fmt.Println(err)
		}
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if debug {
			logger.Error(err.Error())
		}
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.bizfly-backup.yaml)")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug (default is false)")
	rootCmd.PersistentFlags().StringVar(&addr, "addr", "", "listening address of agent server.")
	rootCmd.PersistentFlags().BoolVar(&force, "force", false, "force backup (may cause full disk).")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	newLogger := zap.NewProduction
	if debug {
		newLogger = zap.NewDevelopment
	}
	var err error
	if logger, err = newLogger(); err != nil {
		panic(err)
	}

	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			logger.Error(err.Error())
			os.Exit(1)
		}

		// Search config in home directory with name ".bizfly-backup" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigName(".bizfly-backup")
	}

	// Set default value for config
	viper.SetDefault("port", defaultPort)

	// set value for force
	viper.Set("force", force)

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		logger.Info("Using config file: " + viper.ConfigFileUsed())
	}

	// Set value
	if addr == "" {
		addr = httpPrefix + strings.Join([]string{localhost, viper.GetString("port")}, ":")
	}
}
