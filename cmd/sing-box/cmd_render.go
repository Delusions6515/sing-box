package main

import (
	"bytes"
	"os"

	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing/common/json"

	"github.com/spf13/cobra"
)

var commandRender = &cobra.Command{
	Use:   "render",
	Short: "Render configuration for the current CLI target",
	Run: func(cmd *cobra.Command, args []string) {
		if err := render(); err != nil {
			log.Fatal(err)
		}
	},
	Args: cobra.NoArgs,
}

func init() {
	mainCommand.AddCommand(commandRender)
}

func render() error {
	options, _, err := readConfigAndCheck()
	if err != nil {
		return err
	}
	buffer := new(bytes.Buffer)
	encoder := json.NewEncoder(buffer)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(options); err != nil {
		return err
	}
	_, err = os.Stdout.Write(buffer.Bytes())
	return err
}
