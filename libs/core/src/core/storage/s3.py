"""MinIO/S3 client helpers: presign, upload, download (PRD §6.1).

The API never buffers file bytes — clients PUT directly to MinIO via a
presigned URL; workers download to tmpfs only.
"""

from __future__ import annotations

import boto3
from botocore.client import Config as BotoConfig

from core.config import Settings, get_settings


def make_s3(settings: Settings | None = None):
    s = settings or get_settings()
    return boto3.client(
        "s3",
        endpoint_url=s.s3_endpoint,
        aws_access_key_id=s.s3_access_key,
        aws_secret_access_key=s.s3_secret_key,
        config=BotoConfig(signature_version="s3v4"),
    )


def raw_key(doc_id: str) -> str:
    """Object key for an uploaded source PDF: s3://raw/{doc_id}.pdf."""
    return f"{doc_id}.pdf"


def parsed_key(doc_id: str, shard_idx: int) -> str:
    """Object key for a parsed shard markdown: s3://parsed/{doc}/{idx}.md."""
    return f"{doc_id}/{shard_idx}.md"


def presign_put(s3, bucket: str, key: str, expires_s: int = 3600) -> str:
    return s3.generate_presigned_url(
        "put_object",
        Params={"Bucket": bucket, "Key": key},
        ExpiresIn=expires_s,
    )


def download_to(s3, bucket: str, key: str, dest_path: str) -> None:
    s3.download_file(bucket, key, dest_path)


def upload_text(s3, bucket: str, key: str, text: str, content_type: str = "text/markdown") -> None:
    s3.put_object(Bucket=bucket, Key=key, Body=text.encode("utf-8"), ContentType=content_type)


# Historical name kept as an alias — old callers may still reference it.
upload_json = upload_text
