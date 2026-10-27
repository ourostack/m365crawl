package store

import "time"

// WorkIQSourceState keeps live and explicit-deletion versions in their own source domain.
type WorkIQSourceState struct {
	Seen                                                         bool
	SeenAt, FirstMissAt, GoneAt, EvictedAt, DeletedAt, VersionAt *time.Time
	DeletedVersionAt                                             *time.Time
	Version, DeletedVersion                                      int64
	Text                                                         string
}

type WorkIQReduceInput struct {
	Cache, Server          WorkIQSourceState
	CrossVersionComparable bool
}

type WorkIQEffectiveState struct {
	Provenance, Winner           string
	GoneAt, EvictedAt, DeletedAt *time.Time
	Text                         string
	VersionUnverified            bool
	UnknownFirstMiss             bool
}

func reduceWorkIQState(in WorkIQReduceInput) WorkIQEffectiveState {
	out := WorkIQEffectiveState{Provenance: "desktop_cache", Winner: "desktop_cache", Text: in.Cache.Text}
	switch {
	case in.Cache.Seen && in.Server.Seen:
		out.Provenance = "both"
	case in.Server.Seen:
		out.Provenance, out.Winner, out.Text = "workiq_server", "workiq_server", in.Server.Text
	}
	if in.Cache.Seen && in.Server.Seen {
		out.VersionUnverified = !in.CrossVersionComparable && in.Cache.Text != "" && in.Server.Text != "" && in.Cache.Text != in.Server.Text
		if in.Server.Text != "" && (in.Cache.Text == "" || in.CrossVersionComparable &&
			workIQLaterVersion(in.Server.Version, in.Server.VersionAt, in.Cache.Version, in.Cache.VersionAt)) {
			out.Winner, out.Text = "workiq_server", in.Server.Text
		}
	}
	cacheDeleted, serverDeleted := workIQOwnDeletion(in.Cache), workIQOwnDeletion(in.Server)
	serverOwnDeleted := serverDeleted
	if in.CrossVersionComparable {
		if cacheDeleted != nil && serverDeleted == nil && in.Server.Seen &&
			workIQLaterVersion(in.Server.Version, in.Server.VersionAt, in.Cache.DeletedVersion, in.Cache.DeletedVersionAt) {
			cacheDeleted = nil
		}
		if serverDeleted != nil && cacheDeleted == nil && in.Cache.Seen &&
			workIQLaterVersion(in.Cache.Version, in.Cache.VersionAt, in.Server.DeletedVersion, in.Server.DeletedVersionAt) {
			serverDeleted = nil
		}
	}
	out.DeletedAt = cacheDeleted
	if out.DeletedAt == nil {
		out.DeletedAt = serverDeleted
	}
	if in.Cache.Seen {
		var sight *time.Time
		if in.Server.Seen && serverOwnDeleted == nil && in.Server.GoneAt == nil && in.Server.SeenAt != nil && !in.Server.SeenAt.IsZero() {
			sight = in.Server.SeenAt
		}
		var unknownGone, unknownEvicted bool
		out.GoneAt, unknownGone = workIQAbsence(in.Cache.GoneAt, in.Cache.FirstMissAt, sight)
		out.EvictedAt, unknownEvicted = workIQAbsence(in.Cache.EvictedAt, in.Cache.FirstMissAt, sight)
		out.UnknownFirstMiss = unknownGone || unknownEvicted
	} else if in.Server.Seen {
		out.GoneAt = in.Server.GoneAt
	}
	return out
}

func workIQLaterVersion(version int64, at *time.Time, oldVersion int64, oldAt *time.Time) bool {
	if at != nil && oldAt != nil {
		return !at.IsZero() && !oldAt.IsZero() && at.After(*oldAt)
	}
	return version > 0 && oldVersion > 0 && version > oldVersion
}

func workIQOwnDeletion(source WorkIQSourceState) *time.Time {
	if !source.Seen || source.DeletedAt == nil ||
		workIQLaterVersion(source.Version, source.VersionAt, source.DeletedVersion, source.DeletedVersionAt) {
		return nil
	}
	return source.DeletedAt
}

func workIQAbsence(mark, firstMiss, serverLive *time.Time) (*time.Time, bool) {
	if mark == nil || serverLive == nil {
		return mark, false
	}
	boundary := firstMiss
	known := boundary != nil && !boundary.IsZero()
	if !known {
		boundary = mark
	}
	if serverLive.After(*boundary) {
		return nil, false
	}
	return mark, !known
}
