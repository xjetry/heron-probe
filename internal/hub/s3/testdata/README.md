# AWS SigV4 测试向量

原样复制自 AWS 维护的 boto/botocore 测试套件，固定版本：
https://github.com/boto/botocore/tree/32302bc372dde1b6173b60f8b85d671e24a0d414/tests/unit/auth/aws4_testsuite

上游许可为 Apache-2.0。每组保留原始 `.req`、`.creq`、`.sts`、`.authz`，不得由被测代码重算期望值。
向量的区域为 `us-east-1`，服务为 `service`，access key 为 `AKIDEXAMPLE`，
secret 为 `wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY`；它们是公开测试凭据。
通用向量不含 S3 的 `x-amz-content-sha256` 头；S3 出站测试另验该头。
