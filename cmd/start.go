/*
Package cmd includes relayer commands
Copyright © 2020 Jack Zampolin <jack.zampolin@gmail.com>

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	retry "github.com/avast/retry-go"
	"github.com/cosmos/relayer/relayer"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

// var (
// 	// defaultTime to return when there is error or client didn't exists.
// 	defaultTime time.Duration = time.Minute * 1
// )

// startCmd represents the start command
// NOTE: This is basically psuedocode
func startCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "start [path-name]",
		Aliases: []string{"st"},
		Short:   "Start the listening relayer on a given path",
		Args:    cobra.ExactArgs(1),
		Example: strings.TrimSpace(fmt.Sprintf(`
$ %s start demo-path --max-msgs 3
$ %s start demo-path2 --max-tx-size 10`, appName, appName)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, src, dst, err := config.ChainsFromPath(args[0])
			if err != nil {
				return err
			}

			if err = ensureKeysExist(c); err != nil {
				return err
			}

			path := config.Paths.MustGet(args[0])
			strategy, err := GetStrategyWithOptions(cmd, path.MustGetStrategy())
			if err != nil {
				return err
			}

			if relayer.SendToController != nil {
				action := relayer.PathAction{
					Path: path,
					Type: "RELAYER_PATH_START",
				}
				cont, err := relayer.ControllerUpcall(&action)
				if !cont {
					return err
				}
			}

			done, err := relayer.RunStrategy(c[src], c[dst], strategy)
			if err != nil {
				return err
			}

			eg := new(errgroup.Group)
			eg.Go(func() error {
				for {
					var sleepTime time.Duration
					if err := retry.Do(func() error {
						sleepTime, err = UpdateClientsFromChains(c[src], c[dst])
						if err != nil {
							return err
						}
						return nil
					}, retry.Attempts(5), retry.Delay(time.Millisecond*500), retry.LastErrorOnly(true)); err != nil {
						return err
					}
					time.Sleep(sleepTime)
				}
			})

			if err = eg.Wait(); err != nil {
				return err
			}

			trapSignal(done)
			return nil
		},
	}
	return strategyFlag(updateTimeFlags(cmd))
}

// trap signal waits for a SIGINT or SIGTERM and then sends down the done channel
func trapSignal(done func()) {
	sigCh := make(chan os.Signal, 1)

	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// wait for a signal
	sig := <-sigCh
	fmt.Println("Signal Received", sig.String())
	close(sigCh)

	// call the cleanup func
	done()
}

// UpdateClientsFromChains takes src, dst chains and update clients based on expiry time
func UpdateClientsFromChains(src, dst *relayer.Chain) (sleepTime time.Duration, err error) {
	var srcTimeExpiry, dstTimeExpiry time.Duration

	thresholdTime := viper.GetDuration(flagThresholdTime)

	eg := new(errgroup.Group)
	eg.Go(func() error {
		srcTimeExpiry, err = relayer.GetClientAndUpdate(src, dst, thresholdTime)
		return err
	})
	eg.Go(func() error {
		dstTimeExpiry, err = relayer.GetClientAndUpdate(dst, src, thresholdTime)
		return err
	})
	if err := eg.Wait(); err != nil {
		return 0, err
	}

	if srcTimeExpiry > 0 {
		if dstTimeExpiry > 0 {
			if srcTimeExpiry < dstTimeExpiry {
				sleepTime = srcTimeExpiry - thresholdTime

			} else {
				sleepTime = dstTimeExpiry - thresholdTime
			}
		} else {
			sleepTime = srcTimeExpiry - thresholdTime
		}
	} else {
		if dstTimeExpiry > 0 {
			sleepTime = dstTimeExpiry - thresholdTime
		} else {
			return 0, fmt.Errorf("seems clients of both src:%s and dst:%s are expired",
				src.PathEnd.ChainID, dst.PathEnd.ChainID)
		}
	}
	return sleepTime, nil
}
