## neddns: authoritative DNS server backed by S3

### Features:
- serves zone files from AWS S3 for simple high availability
- reload zones from S3 on a configurable schedule
- hot-reload zones with a HUP signal
- supports root CNAME flatting
- deployed as a single binary

```
Usage:
	neddns [options] <bucket>
	neddns -h --help
	neddns --version

AWS Authentication:
  Either use the -K and -S flags, or
  set the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.

Options:
  -K, --awskey=<keyid>      AWS key ID (or use AWS_ACCESS_KEY_ID environemnt variable).
  -S, --awssecret=<secret>  AWS secret key (or use AWS_SECRET_ACCESS_KEY environemnt variable).
  -r, --region=<region>     AWS region [default: us-east-1].
  -u, --update=<secs>       Frequency to fetch updated zones from S3 in seconds [default: 300].
  -p, --port=<port>         Listen port [default: 53].
  -l, --log=<path>          Write to file at this loctation rather than stdout.
  -d, --debug               Enable debugging output.
  -h, --help                Show this screen.
  --version                 Show version.
```
