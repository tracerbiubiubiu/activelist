package config

import "github.com/spf13/viper"

// B3 enables a single approved ActiveList integration profile.
type B3 struct {
	TypeName          string   `mapstructure:"type_name"`
	AnchorField       string   `mapstructure:"anchor_field"`
	AnchorValue       string   `mapstructure:"anchor_value"`
	WriteFields       []string `mapstructure:"write_fields"`
	ReadFields        []string `mapstructure:"read_fields"`
	EventFields       []string `mapstructure:"event_fields"`
	ProducerAK        string   `mapstructure:"producer_ak"`
	CoreAK            string   `mapstructure:"core_ak"`
	SubjectID         string   `mapstructure:"subject_id"`
	ReadSubjectID     string   `mapstructure:"read_subject_id"`
	ReadGrantRef      string   `mapstructure:"read_grant_ref"`
	ReadGrantRevision string   `mapstructure:"read_grant_revision"`
	GrantRef          string   `mapstructure:"grant_ref"`
	GrantRevision     string   `mapstructure:"grant_revision"`
	ConsumerID        string   `mapstructure:"consumer_id"`
	PushURL           string   `mapstructure:"push_url"`
	PushAK            string   `mapstructure:"push_ak"`
	PushSK            string   `mapstructure:"push_sk"`
}

func bindB3(v *viper.Viper) {
	for key, env := range map[string]string{
		"type_name": "TYPE_NAME", "anchor_field": "ANCHOR_FIELD", "anchor_value": "ANCHOR_VALUE",
		"write_fields": "WRITE_FIELDS", "read_fields": "READ_FIELDS", "event_fields": "EVENT_FIELDS",
		"producer_ak": "PRODUCER_AK", "core_ak": "CORE_AK", "grant_ref": "GRANT_REF",
		"grant_revision": "GRANT_REVISION", "subject_id": "SUBJECT_ID",
		"read_subject_id": "READ_SUBJECT_ID", "read_grant_ref": "READ_GRANT_REF", "read_grant_revision": "READ_GRANT_REVISION", "consumer_id": "CONSUMER_ID", "push_url": "PUSH_URL",
		"push_ak": "PUSH_AK", "push_sk": "PUSH_SK",
	} {
		_ = v.BindEnv("b3."+key, "ACTIVELIST_B3_"+env)
	}
}
