package ca

import (
	"crypto/rand"
	"crypto/x509"
	"io"
	"os"
	"strings"
	"time"

	"github.com/smallstep/certificates/cas/apiv1"
	"github.com/smallstep/certificates/pki"
	"github.com/smallstep/cli/command"
	"github.com/smallstep/cli/crypto/pemutil"
	"github.com/smallstep/cli/errs"
	"github.com/smallstep/cli/ui"
	"github.com/smallstep/cli/utils"
	"github.com/urfave/cli"
)

func initCommand() cli.Command {
	return cli.Command{
		Name:   "init",
		Action: cli.ActionFunc(initAction),
		Usage:  "initialize the CA PKI",
		UsageText: `**step ca init**
[**--root**=<file>] [**--key**=<file>] [**--pki**] [**--ssh**] [**--name**=<name>]
[**--dns**=<dns>] [**--address**=<address>] [**--provisioner**=<name>]
[**--provisioner-password-file**=<file>] [**--password-file**=<file>]
[**--with-ca-url**=<url>] [**--no-db**]`,
		Description: `**step ca init** command initializes a public key infrastructure (PKI) to be
 used by the Certificate Authority.`,
		Flags: []cli.Flag{
			cli.StringFlag{
				Name:   "root",
				Usage:  "The path of an existing PEM <file> to be used as the root certificate authority.",
				EnvVar: command.IgnoreEnvVar,
			},
			cli.StringFlag{
				Name:   "key",
				Usage:  "The path of an existing key <file> of the root certificate authority.",
				EnvVar: command.IgnoreEnvVar,
			},
			cli.BoolFlag{
				Name:  "pki",
				Usage: "Generate only the PKI without the CA configuration.",
			},
			cli.BoolFlag{
				Name:  "ssh",
				Usage: `Create keys to sign SSH certificates.`,
			},
			cli.BoolFlag{
				Name:  "helm",
				Usage: `Generates a Helm values YAML to be used with step-certificates chart.`,
			},
			cli.StringFlag{
				Name: "deployment-type",
				Usage: `The <name> of an the deployment type to use. Options are:
    **standalone**
    :  A standalone CA is the classic configuration where all the options are
    managed between the ca.json and the db.

    **linkedca**
    :  A linked CA is a deployment type where the keys are managed in **step-ca**
    but the provisioners and admins are managed in the cloud.

    **hosted**
    :  A hosted CA is a deployment type where the keys, provisioners, and all
    components are managed in the cloud.`,
			},
			cli.StringFlag{
				Name:  "name",
				Usage: "The <name> of the new PKI.",
			},
			cli.StringFlag{
				Name:  "dns",
				Usage: "The comma separated DNS <names> or IP addresses of the new CA.",
			},
			cli.StringFlag{
				Name:  "address",
				Usage: "The <address> that the new CA will listen at.",
			},
			cli.StringFlag{
				Name:  "provisioner",
				Usage: "The <name> of the first provisioner.",
			},
			cli.StringFlag{
				Name:  "password-file",
				Usage: `The path to the <file> containing the password to encrypt the keys.`,
			},
			cli.StringFlag{
				Name:  "provisioner-password-file",
				Usage: `The path to the <file> containing the password to encrypt the provisioner key.`,
			},
			cli.StringFlag{
				Name:  "with-ca-url",
				Usage: `<URI> of the Step Certificate Authority to write in defaults.json`,
			},
			cli.StringFlag{
				Name:  "ra",
				Usage: `The registration authority <name> to use. Currently "StepCAS" and "CloudCAS" are supported.`,
			},
			cli.StringFlag{
				Name: "issuer",
				Usage: `The registration authority issuer <url> to use.

: If StepCAS is used, this flag should be the URL of the CA to connect
to, e.g https://ca.smallstpe.com:9000

: If CloudCAS is used, this flag should be the resource name of the
intermediate certificate to use. This has the format
'projects/\\*/locations/\\*/caPools/\\*/certificateAuthorities/\\*'.`,
			},
			cli.StringFlag{
				Name: "issuer-fingerprint",
				Usage: `The root certificate <fingerprint> of the issuer CA.
This flag is supported in "StepCAS", and it should be the result of running:
'''
$ step certificate fingerprint root_ca.crt
4fe5f5ef09e95c803fdcb80b8cf511e2a885eb86f3ce74e3e90e62fa3faf1531
'''`,
			},
			cli.StringFlag{
				Name: "issuer-provisioner",

				Usage: `The <name> of an existing provisioner in the issuer CA.
This flag is supported in "StepCAS".`,
			},
			cli.StringFlag{
				Name: "credentials-file",
				Usage: `The registration authority credentials <file> to use.

: If CloudCAS is used, this flag should be the path to a service account key.
It can also be set using the 'GOOGLE_APPLICATION_CREDENTIALS=path'
environment variable or the default service account in an instance in Google
Cloud.`,
			},
			cli.BoolFlag{
				Name:  "no-db",
				Usage: `Generate a CA configuration without the DB stanza. No persistence layer.`,
			},
		},
	}
}

func initAction(ctx *cli.Context) (err error) {
	if err = assertCryptoRand(); err != nil {
		return err
	}

	var rootCrt *x509.Certificate
	var rootKey interface{}

	caURL := ctx.String("with-ca-url")
	root := ctx.String("root")
	key := ctx.String("key")
	ra := strings.ToLower(ctx.String("ra"))
	pkiOnly := ctx.Bool("pki")
	noDB := ctx.Bool("no-db")
	helm := ctx.Bool("helm")

	switch {
	case len(root) > 0 && len(key) == 0:
		return errs.RequiredWithFlag(ctx, "root", "key")
	case len(root) == 0 && len(key) > 0:
		return errs.RequiredWithFlag(ctx, "key", "root")
	case len(root) > 0 && len(key) > 0:
		if rootCrt, err = pemutil.ReadCertificate(root); err != nil {
			return err
		}
		if rootKey, err = pemutil.Read(key); err != nil {
			return err
		}
	case ra != "" && ra != apiv1.CloudCAS && ra != apiv1.StepCAS:
		return errs.InvalidFlagValue(ctx, "ra", ctx.String("ra"), "StepCAS or CloudCAS")
	case pkiOnly && noDB:
		return errs.IncompatibleFlagWithFlag(ctx, "pki", "no-db")
	case pkiOnly && helm:
		return errs.IncompatibleFlagWithFlag(ctx, "pki", "helm")
	}

	var password string
	if passwordFile := ctx.String("password-file"); passwordFile != "" {
		password, err = utils.ReadStringPasswordFromFile(passwordFile)
		if err != nil {
			return err
		}
	}

	// Provisioner password will be equal to the certificate private keys if
	// --provisioner-password-file is not provided.
	var provisionerPassword []byte
	if passwordFile := ctx.String("provisioner-password-file"); passwordFile != "" {
		provisionerPassword, err = utils.ReadPasswordFromFile(passwordFile)
		if err != nil {
			return err
		}
	}

	// Common for both CA and RA

	var name, org, resource string
	var casOptions apiv1.Options
	var deploymentType pki.DeploymentType
	switch ra {
	case apiv1.CloudCAS:
		var create bool
		var project, location, caPool, caPoolTier, gcsBucket string

		caPoolTiers := []struct {
			Name  string
			Value string
		}{{"DevOps", "DEVOPS"}, {"Enterprise", "ENTERPRISE"}}

		// Prompt or get deployment type from flag
		deploymentType, err = promptDeploymentType(ctx, true)
		if err != nil {
			return err
		}

		iss := ctx.String("issuer")
		if iss == "" {
			create, err = ui.PromptYesNo("Would you like to create a new PKI (y) or use an existing one (n)?")
			if err != nil {
				return err
			}
			if create {
				ui.Println("What would you like to name your new PKI?", ui.WithValue(ctx.String("name")))
				name, err = ui.Prompt("(e.g. Smallstep)",
					ui.WithValidateNotEmpty(), ui.WithValue(ctx.String("name")))
				if err != nil {
					return err
				}
				ui.Println("What is the name of your organization?")
				org, err = ui.Prompt("(e.g. Smallstep)",
					ui.WithValidateNotEmpty())
				if err != nil {
					return err
				}
				ui.Println("What resource id do you want to use? [we will append -Root-CA or -Intermediate-CA]")
				resource, err = ui.Prompt("(e.g. Smallstep)",
					ui.WithValidateRegexp("^[a-zA-Z0-9-_]+$"))
				if err != nil {
					return err
				}
				ui.Println("What is the id of your project on Google's Cloud Platform?")
				project, err = ui.Prompt("(e.g. smallstep-ca)",
					ui.WithValidateRegexp("^[a-z][a-z0-9-]{4,28}[a-z0-9]$"))
				if err != nil {
					return err
				}
				ui.Println("What region or location do you want to use?")
				location, err = ui.Prompt("(e.g. us-west1)",
					ui.WithValidateRegexp("^[a-z0-9-]+$"))
				if err != nil {
					return err
				}
				ui.Println("What CA pool name do you want to use?")
				caPool, err = ui.Prompt("(e.g. Smallstep)",
					ui.WithValidateRegexp("^[a-zA-Z0-9_-]{1,63}"))
				if err != nil {
					return err
				}
				i, _, err := ui.Select("What CA pool tier do you want to use?", caPoolTiers, ui.WithSelectTemplates(ui.NamedSelectTemplates("Tier")))
				if err != nil {
					return err
				}
				caPoolTier = caPoolTiers[i].Value
				ui.Println("What GCS bucket do you want to use? Leave it empty to use a managed one.")
				gcsBucket, err = ui.Prompt("(e.g. my-bucket)", ui.WithValidateRegexp("(^$)|(^[a-z0-9._-]{3,222}$)"))
				if err != nil {
					return err
				}
			} else {
				ui.Println("What certificate authority would you like to use?")
				iss, err = ui.Prompt("(e.g. projects/smallstep-ca/locations/us-west1/caPools/smallstep/certificateAuthorities/intermediate-ca)",
					ui.WithValidateRegexp("^projects/[a-z][a-z0-9-]{4,28}[a-z0-9]/locations/[a-z0-9-]+/caPools/[a-zA-Z0-9-_]+/certificateAuthorities/[a-zA-Z0-9-_]+$"))
				if err != nil {
					return err
				}
			}
		}
		casOptions = apiv1.Options{
			Type:                 apiv1.CloudCAS,
			CredentialsFile:      ctx.String("credentials-file"),
			CertificateAuthority: iss,
			IsCreator:            create,
			Project:              project,
			Location:             location,
			CaPool:               caPool,
			CaPoolTier:           caPoolTier,
			GCSBucket:            gcsBucket,
		}
	case apiv1.StepCAS:
		deploymentType, err = promptDeploymentType(ctx, true)
		if err != nil {
			return err
		}
		ui.Println("What is the url of your CA?", ui.WithValue(ctx.String("issuer")))
		ca, err := ui.Prompt("(e.g. https://ca.smallstep.com:9000)",
			ui.WithValidateRegexp("(?i)^https://.+$"), ui.WithValue(ctx.String("issuer")))
		if err != nil {
			return err
		}
		ui.Println("What is the fingerprint of the CA's root file?", ui.WithValue(ctx.String("issuer-fingerprint")))
		fingerprint, err := ui.Prompt("(e.g. 4fe5f5ef09e95c803fdcb80b8cf511e2a885eb86f3ce74e3e90e62fa3faf1531)",
			ui.WithValidateRegexp("^[a-fA-F0-9]{64}$"), ui.WithValue(ctx.String("issuer-fingerprint")))
		if err != nil {
			return err
		}
		ui.Println("What is the JWK provisioner you want to use?", ui.WithValue(ctx.String("issuer-provisioner")))
		provisioner, err := ui.Prompt("(e.g. you@smallstep.com)",
			ui.WithValidateNotEmpty(), ui.WithValue(ctx.String("issuer-provisioner")))
		if err != nil {
			return err
		}
		casOptions = apiv1.Options{
			Type:                            apiv1.StepCAS,
			IsCreator:                       false,
			IsCAGetter:                      true,
			CertificateAuthority:            ca,
			CertificateAuthorityFingerprint: fingerprint,
			CertificateIssuer: &apiv1.CertificateIssuer{
				Type:        "JWK",
				Provisioner: provisioner,
			},
		}
	default:
		deploymentType, err = promptDeploymentType(ctx, false)
		if err != nil {
			return err
		}
		if deploymentType == pki.HostedDeployment {
			ui.Println()
			ui.Println("The initialization of a hosted deployment is not yet supported by this tool.")
			ui.Println("But you can create one at https://smallstep.com/certificate-manager/\n")
			ui.Println("After creating it you can bootstrap it running:\n")
			ui.Println("    $ step ca bootstrap --team <name>")
			ui.Println()
			return nil
		}

		ui.Println("What would you like to name your new PKI?", ui.WithValue(ctx.String("name")))
		name, err = ui.Prompt("(e.g. Smallstep)", ui.WithValidateNotEmpty(), ui.WithValue(ctx.String("name")))
		if err != nil {
			return err
		}
		org = name
		casOptions = apiv1.Options{
			Type:      apiv1.SoftCAS,
			IsCreator: true,
		}
	}

	var opts []pki.PKIOption
	if pkiOnly {
		opts = append(opts, pki.WithPKIOnly())
	} else {
		var names string
		ui.Println("What DNS names or IP addresses would you like to add to your new CA?", ui.WithValue(ctx.String("dns")))
		names, err = ui.Prompt("(e.g. ca.smallstep.com[,1.1.1.1,etc.])",
			ui.WithValidateFunc(ui.DNS()), ui.WithValue(ctx.String("dns")))
		if err != nil {
			return err
		}
		names = strings.Replace(names, " ", ",", -1)
		parts := strings.Split(names, ",")
		var dnsNames []string
		for _, name := range parts {
			if len(name) == 0 {
				continue
			}
			dnsNames = append(dnsNames, strings.TrimSpace(name))
		}

		var address string
		ui.Println("What IP and port will your new CA bind to?", ui.WithValue(ctx.String("address")))
		address, err = ui.Prompt("(e.g. :443 or 127.0.0.1:4343)",
			ui.WithValidateFunc(ui.Address()), ui.WithValue(ctx.String("address")))
		if err != nil {
			return err
		}

		var provisioner string
		// Only standalone deployments with create an initial provisioner.
		// Linked or hosted deployments will use an OIDC token as the first
		// deployment.
		if deploymentType == pki.StandaloneDeployment {
			ui.Println("What would you like to name the CA's first provisioner?", ui.WithValue(ctx.String("provisioner")))
			provisioner, err = ui.Prompt("(e.g. you@smallstep.com)",
				ui.WithValidateNotEmpty(), ui.WithValue(ctx.String("provisioner")))
			if err != nil {
				return err
			}
		}

		opts = []pki.PKIOption{
			pki.WithAddress(address),
			pki.WithCaUrl(caURL),
			pki.WithDNSNames(dnsNames),
			pki.WithDeploymentType(deploymentType),
		}
		if deploymentType == pki.StandaloneDeployment {
			opts = append(opts, pki.WithProvisioner(provisioner))
		}
		if deploymentType == pki.LinkedDeployment {
			opts = append(opts, pki.WithAdmin())
		} else if ctx.Bool("ssh") {
			opts = append(opts, pki.WithSSH())
		}
		if noDB {
			opts = append(opts, pki.WithNoDB())
		}
		if helm {
			opts = append(opts, pki.WithHelm())
		}
	}

	p, err := pki.New(casOptions, opts...)
	if err != nil {
		return err
	}

	// Linked CAs will use OIDC as a first provisioner.
	if pkiOnly || deploymentType != pki.StandaloneDeployment {
		ui.Println("Choose a password for your CA keys.", ui.WithValue(password))
	} else {
		ui.Println("Choose a password for your CA keys and first provisioner.", ui.WithValue(password))
	}

	pass, err := ui.PromptPasswordGenerate("[leave empty and we'll generate one]", ui.WithRichPrompt(), ui.WithValue(password))
	if err != nil {
		return err
	}

	if !pkiOnly && deploymentType == pki.StandaloneDeployment {
		// Generate provisioner key pairs.
		if len(provisionerPassword) > 0 {
			if err = p.GenerateKeyPairs(provisionerPassword); err != nil {
				return err
			}
		} else {
			if err = p.GenerateKeyPairs(pass); err != nil {
				return err
			}
		}
	}

	if casOptions.IsCreator {
		var root *apiv1.CreateCertificateAuthorityResponse
		ui.Println()
		// Generate root certificate if not set.
		if rootCrt == nil && rootKey == nil {
			ui.Print("Generating root certificate... ")
			root, err = p.GenerateRootCertificate(name, org, resource, pass)
			if err != nil {
				return err
			}
			ui.Println("done!")
		} else {
			ui.Printf("Copying root certificate... ")
			// Do not copy key in STEPPATH
			if err = p.WriteRootCertificate(rootCrt, nil, nil); err != nil {
				return err
			}
			root = p.CreateCertificateAuthorityResponse(rootCrt, rootKey)
			ui.Println("done!")
		}

		// Always generate the intermediate certificate
		ui.Printf("Generating intermediate certificate... ")
		time.Sleep(1 * time.Second)
		err = p.GenerateIntermediateCertificate(name, org, resource, root, pass)
		if err != nil {
			return err
		}
		ui.Println("done!")
	} else {
		// Attempt to get the root certificate from RA.
		if err := p.GetCertificateAuthority(); err != nil {
			return err
		}
	}

	if ctx.Bool("ssh") {
		ui.Printf("Generating user and host SSH certificate signing keys... ")
		if err := p.GenerateSSHSigningKeys(pass); err != nil {
			return err
		}
		ui.Println("done!")
	}

	if helm {
		return p.WriteHelmTemplate(os.Stdout)
	}
	return p.Save()
}

func promptDeploymentType(ctx *cli.Context, isRA bool) (pki.DeploymentType, error) {
	type deployment struct {
		Name  string
		Value pki.DeploymentType
	}

	var deploymentTypes []deployment
	if isRA {
		switch v := strings.ToLower(ctx.String("deployment-type")); v {
		case "":
			deploymentTypes = []deployment{
				{"Standalone RA", pki.StandaloneDeployment},
				{"Linked RA", pki.LinkedDeployment},
			}
		case "standalone":
			return pki.StandaloneDeployment, nil
		case "linked":
			return pki.LinkedDeployment, nil
		default:
			return 0, errs.InvalidFlagValue(ctx, "deployment-type", v, "standalone or linked")
		}
	} else {
		switch v := strings.ToLower(ctx.String("deployment-type")); v {
		case "":
			deploymentTypes = []deployment{
				{"Standalone CA", pki.StandaloneDeployment},
				{"Linked CA", pki.LinkedDeployment},
				{"Hosted CA", pki.HostedDeployment},
			}
		case "standalone":
			return pki.StandaloneDeployment, nil
		case "linked":
			return pki.LinkedDeployment, nil
		case "hosted":
			return pki.HostedDeployment, nil
		default:
			return 0, errs.InvalidFlagValue(ctx, "deployment-type", v, "standalone, linked or hosted")
		}
	}

	i, _, err := ui.Select("What deployment type would you like to configure?", deploymentTypes,
		ui.WithSelectTemplates(ui.NamedSelectTemplates("Deployment Type")))
	if err != nil {
		return 0, err
	}
	return deploymentTypes[i].Value, nil
}

// assertCryptoRand asserts that a cryptographically secure random number
// generator is available, it will return an error otherwise.
func assertCryptoRand() error {
	buf := make([]byte, 64)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		return errs.NewError("crypto/rand is unavailable: Read() failed with %#v", err)
	}
	return nil
}
