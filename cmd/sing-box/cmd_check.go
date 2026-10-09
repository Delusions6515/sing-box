package main

import (
	"context"

	"github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	"github.com/spf13/cobra"
)

var commandCheck = &cobra.Command{
	Use:   "check",
	Short: "Check configuration",
	Run: func(cmd *cobra.Command, args []string) {
		err := check()
		if err != nil {
			log.Fatal(err)
		}
	},
	Args: cobra.NoArgs,
}

func init() {
	mainCommand.AddCommand(commandCheck)
}

func check() error {
	_, _, err := readConfigAndCheck()
	return err
}

func readConfigAndCheck() (option.Options, []*OptionsEntry, error) {
	optionsList, err := readConfig()
	if err != nil {
		return option.Options{}, nil, err
	}
	options, err := mergeOptionsList(optionsList)
	if err != nil {
		return option.Options{}, optionsList, wrapCLIConfigSources(optionsList, err)
	}
	ctx, cancel := context.WithCancel(service.ExtendContext(globalCtx))
	instance, err := box.New(box.Options{
		Context: ctx,
		Options: options,
	})
	if err == nil {
		instance.Close()
	} else {
		err = wrapCLIConfigSources(optionsList, err)
	}
	cancel()
	return options, optionsList, err
}
