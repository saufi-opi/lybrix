"""core — shared domain library for lybrix.

Everything here is importable by every service; nothing here imports a
service backwards (PRD §12). No service-specific logic lives in this
package: only config, persistence models, queue contracts, storage
helpers, observability plumbing, and the error taxonomy.
"""
