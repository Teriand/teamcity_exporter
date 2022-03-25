package main

import (
	"fmt"
//	"io/ioutil"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

func (c *Configuration) parseConfig(path string) error {
	
	yamlReader, err := os.Open(path)
	if err != nil {
	fmt.Errorf("parseConfig %q", err)
	return  err
	}
	defer yamlReader.Close()
	decoder := yaml.NewDecoder(yamlReader)
	decoder.KnownFields(true)
	
	err = decoder.Decode(&c)
	fmt.Errorf("parseConfig %q", err)
	return  err
}

func (c *Configuration) validateConfig() error {
	for i := range c.Instances {
		if c.Instances[i].Name == "" {
			return fmt.Errorf("Configuration error. Field 'name' is empty for instance %d", i)
		}
		if c.Instances[i].URL == "" {
			return fmt.Errorf("Configuration error. Field 'url' is empty for instance '%s'", c.Instances[i].Name)
		}
		re := regexp.MustCompile("^http://|https://")
		if !re.Match([]byte(c.Instances[i].URL)) {
			return fmt.Errorf("Configuration error. Field 'url' must start from http:// or https:// prefix in instance '%s'", c.Instances[i].Name)
		}
		if c.Instances[i].Username == "" {
			return fmt.Errorf("Configuration error. Field 'username' is empty for instance '%s'", c.Instances[i].Name)
		}
		if c.Instances[i].Password == "" {
			return fmt.Errorf("Configuration error. Field 'password' is empty for instance '%s'", c.Instances[i].Name)
		}
		if c.Instances[i].ScrapeInterval == 0 {
			return fmt.Errorf("Configuration error. Field 'scrape_interval' is empty for instance '%s'", c.Instances[i].Name)
		}

		for v := range c.Instances[i].BuildsFilters {
			for k := range c.Instances[i].BuildsFilters {
				if v == k {
					continue
				}
				if c.Instances[i].BuildsFilters[v].Name == c.Instances[i].BuildsFilters[k].Name {
					return fmt.Errorf("Configuration error. Several filters in instance '%s' have the same name '%s'", c.Instances[i].Name, c.Instances[i].BuildsFilters[v].Name)
				}
			}
		}
	}
	return nil
}
