package outlookmeetingattendees

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

func censusOf(stats hxstore.Stats) Census {
	var census Census
	dst, src := reflect.ValueOf(&census).Elem(), reflect.ValueOf(stats)
	for i := range dst.NumField() {
		field := dst.Field(i)
		name := dst.Type().Field(i).Name
		value := src.FieldByName(name)
		switch field.Kind() {
		case reflect.Int64:
			field.SetInt(value.Int())
		case reflect.Slice:
			field.Set(reflect.MakeSlice(field.Type(), 0, value.Len()))
			for _, key := range value.MapKeys() {
				count := value.MapIndex(key).Int()
				switch field.Type().Elem() {
				case reflect.TypeFor[PairCount]():
					pair := key.Interface().(hxstore.Pair)
					field.Set(reflect.Append(field, reflect.ValueOf(PairCount{pair.Class, pair.Tag, count})))
				case reflect.TypeFor[IntCount]():
					field.Set(reflect.Append(field, reflect.ValueOf(IntCount{key.Int(), count})))
				case reflect.TypeFor[EnumCount]():
					field.Set(reflect.Append(field, reflect.ValueOf(EnumCount{key.String(), count})))
				}
			}
		}
	}
	for _, rows := range []*[]PairCount{&census.Pairs, &census.PairsResynced, &census.PairsResyncedLong} {
		sort.Slice(*rows, func(i, j int) bool {
			a, b := (*rows)[i], (*rows)[j]
			return a.Class < b.Class || (a.Class == b.Class && a.Tag < b.Tag)
		})
	}
	for _, rows := range []*[]IntCount{&census.HeadLens, &census.GapLens, &census.TrailerHeadLens, &census.Gap1Values, &census.Gap11OneByteDiff} {
		sort.Slice(*rows, func(i, j int) bool { return (*rows)[i].Key < (*rows)[j].Key })
	}
	for _, rows := range []*[]EnumCount{&census.Rejected, &census.NoObjectFirst4, &census.HeadFirst4, &census.GapFirst4} {
		sort.Slice(*rows, func(i, j int) bool { return (*rows)[i].Key < (*rows)[j].Key })
	}
	return census
}

func validateCensus(c Census) error {
	v := reflect.ValueOf(c)
	for i := range v.NumField() {
		field := v.Field(i)
		if field.Kind() == reflect.Int64 && field.Int() < 0 {
			return ErrInvalid
		}
	}
	for index, group := range []struct {
		rows   []PairCount
		parent []PairCount
	}{
		{c.Pairs, nil}, {c.PairsResynced, c.Pairs}, {c.PairsResyncedLong, c.PairsResynced},
	} {
		if len(group.rows) > 4096 {
			return ErrTooLarge
		}
		for i, row := range group.rows {
			if row.Count <= 0 {
				return ErrInvalid
			}
			if i > 0 {
				prev := group.rows[i-1]
				if prev.Class > row.Class || (prev.Class == row.Class && prev.Tag >= row.Tag) {
					return ErrInvalid
				}
			}
			if index > 0 {
				found := false
				for _, parent := range group.parent {
					if row.Class == parent.Class && row.Tag == parent.Tag && row.Count <= parent.Count {
						found = true
						break
					}
				}
				if !found {
					return ErrInvalid
				}
			}
		}
	}
	for _, group := range []struct {
		rows []IntCount
		max  int64
	}{
		{c.HeadLens, 64}, {c.GapLens, 64}, {c.TrailerHeadLens, 4097},
		{c.Gap1Values, 255}, {c.Gap11OneByteDiff, 10},
	} {
		if len(group.rows) > int(group.max)+1 {
			return ErrTooLarge
		}
		for i, row := range group.rows {
			if row.Key < 0 || row.Key > group.max || row.Count <= 0 || (i > 0 && group.rows[i-1].Key >= row.Key) {
				return ErrInvalid
			}
		}
	}
	classes := []string{"large", "medium", "small", "zero"}
	rejections := []string{hxstore.RejectMagic, hxstore.RejectTruncated, hxstore.RejectHeaderUnknown, hxstore.RejectTypeOther, hxstore.RejectOversize, hxstore.RejectHeaderCRC, hxstore.RejectPayloadCRC, hxstore.RejectInflate}
	for _, group := range []struct {
		rows    []EnumCount
		allowed []string
	}{
		{c.Rejected, rejections}, {c.NoObjectFirst4, classes}, {c.HeadFirst4, classes}, {c.GapFirst4, classes},
	} {
		if len(group.rows) > len(group.allowed) {
			return ErrTooLarge
		}
		for i, row := range group.rows {
			if !slices.Contains(group.allowed, row.Key) || row.Count <= 0 || (i > 0 && group.rows[i-1].Key >= row.Key) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func EncodeCensus(ctx context.Context, c Census) ([]byte, error) {
	return encodeCensus(ctx, c, MaxCensusBytes)
}

func encodeCensus(ctx context.Context, c Census, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateCensus(c); err != nil {
		return nil, err
	}
	// Nil and empty owned projections have the same canonical array spelling.
	value := reflect.ValueOf(&c).Elem()
	for i := range value.NumField() {
		f := value.Field(i)
		if f.Kind() == reflect.Slice && f.IsNil() {
			f.Set(reflect.MakeSlice(f.Type(), 0, 0))
		}
	}
	raw, _ := json.Marshal(c) // fixed numeric/string owned projection only
	if len(raw) > limit {
		return nil, ErrTooLarge
	}
	return raw, ctx.Err()
}

func DecodeCensus(ctx context.Context, raw []byte) (Census, error) {
	if len(raw) > MaxCensusBytes {
		return Census{}, ErrTooLarge
	}
	var c Census
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Census{}, ErrInvalid
	}
	canonical, err := EncodeCensus(ctx, c)
	if err != nil {
		return Census{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Census{}, ErrInvalid
	}
	return c, nil
}
