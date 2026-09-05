import {canonicalHouseholdItems,CatalogFacets, HouseholdItem, HouseholdState} from '@filelist/shared';

export type HouseholdSection = {key:'continue'|'favorites'|'recent'|'watched';title:string;items:HouseholdItem[]};

export function householdSections(route:string,state:HouseholdState):HouseholdSection[]{
  if(route==='home')return [{key:'continue',title:'Continue watching',items:canonicalHouseholdItems(state.continueWatching)},{key:'favorites',title:'Favorites',items:canonicalHouseholdItems(state.favorites)}];
  if(route==='library')return [{key:'continue',title:'Continue watching',items:canonicalHouseholdItems(state.continueWatching)},{key:'recent',title:'Recently viewed',items:canonicalHouseholdItems(state.recent)},{key:'watched',title:'Watched',items:canonicalHouseholdItems(state.watched)}];
  if(route==='continue')return [{key:'continue',title:'Continue watching',items:canonicalHouseholdItems(state.continueWatching)}];
  if(route==='favorites')return [{key:'favorites',title:'Favorites',items:canonicalHouseholdItems(state.favorites)}];
  if(route==='watched')return [{key:'watched',title:'Watched',items:canonicalHouseholdItems(state.watched)}];
  return [];
}

export function trackerCategories(facets:CatalogFacets):string[]{return facets.categories;}
